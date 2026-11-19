package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Plan step 5.2: the ingest webhook routes each signed GitHub delivery to the
// queues of the agents it concerns, once per delivery id:
//   issues.opened -> jobs (triage) + jobs_issue (issue-drafter; humans only)
//   pull_request opened/synchronize/reopened/ready_for_review -> jobs_review
// Everything else, and every replay, enqueues nothing.

var ingestQueues = []string{"jobs", "jobs_issue", "jobs_review"}

const (
	prHead = "1111111111111111111111111111111111111111"
	prBase = "2222222222222222222222222222222222222222"
)

type ingestHarness struct {
	harness
	secret string
	prefix string // delivery ids and repos are unique per test run
	repo   string
}

func setupIngest(t *testing.T) ingestHarness {
	t.Helper()
	h := setup(t)
	secret := os.Getenv("TEST_INGEST_WEBHOOK_SECRET")
	if secret == "" {
		t.Skip("TEST_INGEST_WEBHOOK_SECRET not set; run scripts/test-effector.sh")
	}
	prefix := fmt.Sprintf("e2e-ingest-%d-", time.Now().UnixNano())
	ih := ingestHarness{harness: h, secret: secret, prefix: prefix, repo: "tierbound-e2e/ingest-" + prefix[11:len(prefix)-1]}
	t.Cleanup(func() {
		// Not t.Context(): it is already canceled when cleanups run, and a
		// leftover message would feed the propose tests' queue reads.
		ctx := context.Background()
		var failed []string
		stmts := []string{
			`delete from pgmq.q_jobs where message->>'delivery_id' like $1`,
			`delete from pgmq.q_jobs_issue where message->>'delivery_id' like $1`,
			`delete from pgmq.q_jobs_review where message->>'delivery_id' like $1`,
			`delete from public.ingest_job_keys where delivery_id like $1`,
			`delete from public.webhook_deliveries where delivery_id like $1`,
		}
		for _, stmt := range stmts {
			if _, err := h.db.Exec(ctx, stmt, prefix+"%"); err != nil {
				failed = append(failed, err.Error())
			}
		}
		if len(failed) > 0 {
			t.Errorf("ingest cleanup: %v", failed)
		}
	})
	return ih
}

type ingestReply struct {
	Status   int
	OK       bool     `json:"ok"`
	Enqueued int      `json:"enqueued"`
	Queues   []string `json:"queues"`
	Ignored  string   `json:"ignored"`
	Error    string   `json:"error"`
}

func (ih ingestHarness) deliver(t *testing.T, event, delivery string, payload any) ingestReply {
	t.Helper()
	body := []byte(mustJSON(t, payload))
	mac := hmac.New(sha256.New, []byte(ih.secret))
	mac.Write(body)
	return ih.post(t, event, delivery, body, "sha256="+hex.EncodeToString(mac.Sum(nil)))
}

func (ih ingestHarness) post(t *testing.T, event, delivery string, body []byte, signature string) ingestReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ih.supabaseURL+"/functions/v1/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := ingestReply{Status: resp.StatusCode}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("ingest %s %s: %d, undecodable body: %v", event, delivery, resp.StatusCode, err)
	}
	return r
}

// queued returns the messages each queue holds for one delivery id.
func (ih ingestHarness) queued(t *testing.T, delivery string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, q := range ingestQueues {
		rows, err := ih.db.Query(t.Context(), `select message from pgmq.q_`+q+` where message->>'delivery_id' = $1`, delivery)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var m map[string]any
			if err := rows.Scan(&m); err != nil {
				t.Fatal(err)
			}
			out[q] = append(out[q], m)
		}
		rows.Close()
	}
	return out
}

// wantQueued asserts the exact per-queue message counts for one delivery.
func (ih ingestHarness) wantQueued(t *testing.T, delivery string, want map[string]int) map[string][]map[string]any {
	t.Helper()
	got := ih.queued(t, delivery)
	for _, q := range ingestQueues {
		if len(got[q]) != want[q] {
			t.Fatalf("delivery %s: %d messages on %s, want %d (all: %v)", delivery, len(got[q]), q, want[q], got)
		}
	}
	return got
}

func (ih ingestHarness) issue(action string, number int, user map[string]any, body string) map[string]any {
	return map[string]any{
		"action":     action,
		"issue":      map[string]any{"number": number, "user": user, "body": body},
		"repository": map[string]any{"full_name": ih.repo},
	}
}

func (ih ingestHarness) pull(action string, number int, edit func(pr map[string]any)) map[string]any {
	pr := map[string]any{
		"number": number, "state": "open", "draft": false,
		"user": map[string]any{"login": "octocat", "type": "User"},
		"head": map[string]any{"sha": prHead, "repo": map[string]any{"full_name": ih.repo}},
		"base": map[string]any{"sha": prBase, "repo": map[string]any{"full_name": ih.repo}},
	}
	if edit != nil {
		edit(pr)
	}
	return map[string]any{"action": action, "pull_request": pr, "repository": map[string]any{"full_name": ih.repo}}
}

var human = map[string]any{"login": "octocat", "type": "User"}

func TestIngestIssueOpenedFeedsTriageAndDrafter(t *testing.T) {
	ih := setupIngest(t)
	d := ih.prefix + "issue"
	r := ih.deliver(t, "issues", d, ih.issue("opened", 41, human, "The worker crashes on start."))
	if r.Status != 200 || r.Enqueued != 2 {
		t.Fatalf("ingest: %+v, want 200 enqueued 2", r)
	}
	got := ih.wantQueued(t, d, map[string]int{"jobs": 1, "jobs_issue": 1})
	want := map[string]any{"repo": ih.repo, "issue_number": float64(41), "delivery_id": d}
	for _, q := range []string{"jobs", "jobs_issue"} {
		if fmt.Sprint(got[q][0]) != fmt.Sprint(want) {
			t.Fatalf("%s message = %v, want %v", q, got[q][0], want)
		}
	}
}

func TestIngestPullRequestFeedsReview(t *testing.T) {
	ih := setupIngest(t)
	for i, action := range []string{"opened", "synchronize", "reopened", "ready_for_review"} {
		d := fmt.Sprintf("%spr-%s", ih.prefix, action)
		number := 70 + i // a fresh PR each, so the per-head dedupe does not apply
		r := ih.deliver(t, "pull_request", d, ih.pull(action, number, nil))
		if r.Status != 200 || r.Enqueued != 1 {
			t.Fatalf("%s: %+v, want 200 enqueued 1", action, r)
		}
		got := ih.wantQueued(t, d, map[string]int{"jobs_review": 1})
		want := map[string]any{"repo": ih.repo, "pr_number": float64(number), "head_sha": prHead, "base_sha": prBase, "delivery_id": d}
		if fmt.Sprint(got["jobs_review"][0]) != fmt.Sprint(want) {
			t.Fatalf("%s: message = %v, want %v", action, got["jobs_review"][0], want)
		}
	}
}

func TestIngestIgnoredEventsEnqueueNothing(t *testing.T) {
	ih := setupIngest(t)
	bot := map[string]any{"login": "tierbound[bot]", "type": "Bot"}
	cases := []struct {
		name, event string
		payload     any
		wantStatus  int
	}{
		{"ping", "ping", map[string]any{"zen": "Keep it logically awesome."}, 200},
		{"push", "push", map[string]any{"ref": "refs/heads/main"}, 200},
		{"issue closed", "issues", ih.issue("closed", 42, human, ""), 200},
		{"issue edited", "issues", ih.issue("edited", 42, human, ""), 200},
		{"pr closed", "pull_request", ih.pull("closed", 80, func(pr map[string]any) { pr["state"] = "closed" }), 200},
		{"pr labeled", "pull_request", ih.pull("labeled", 80, nil), 200},
		{"pr draft", "pull_request", ih.pull("opened", 81, func(pr map[string]any) { pr["draft"] = true }), 200},
		{"pr by bot", "pull_request", ih.pull("opened", 82, func(pr map[string]any) { pr["user"] = bot }), 200},
		{"pr by dependabot login", "pull_request", ih.pull("opened", 83, func(pr map[string]any) {
			pr["user"] = map[string]any{"login": "dependabot[bot]", "type": "User"}
		}), 200},
		{"pr from fork", "pull_request", ih.pull("opened", 84, func(pr map[string]any) {
			pr["head"] = map[string]any{"sha": prHead, "repo": map[string]any{"full_name": "someone/fork"}}
		}), 200},
		{"pr from deleted fork", "pull_request", ih.pull("opened", 85, func(pr map[string]any) {
			pr["head"] = map[string]any{"sha": prHead, "repo": nil}
		}), 200},
		{"malformed pr", "pull_request", ih.pull("opened", 86, func(pr map[string]any) {
			pr["head"] = map[string]any{"sha": "not-a-sha", "repo": map[string]any{"full_name": ih.repo}}
		}), 400},
	}
	for i, c := range cases {
		d := fmt.Sprintf("%signored-%d", ih.prefix, i)
		r := ih.deliver(t, c.event, d, c.payload)
		if r.Status != c.wantStatus || r.Enqueued != 0 {
			t.Fatalf("%s: %+v, want %d and nothing enqueued", c.name, r, c.wantStatus)
		}
		ih.wantQueued(t, d, map[string]int{})
	}
}

// Bot-authored and tierbound's own issues still get the v1 triage job, but
// the issue-drafter never drafts from them (no loops on its own output).
func TestIngestBotAndOwnIssuesSkipDrafter(t *testing.T) {
	ih := setupIngest(t)
	cases := map[string]map[string]any{
		"bot":   ih.issue("opened", 43, map[string]any{"login": "tierbound[bot]", "type": "Bot"}, "drafted"),
		"own":   ih.issue("opened", 44, human, "Drafted issue.\n\n<!-- tierbound:3c1f0e8a-0000-4000-8000-000000000000 -->"),
		"login": ih.issue("opened", 45, map[string]any{"login": "renovate[bot]"}, ""),
	}
	for name, payload := range cases {
		d := ih.prefix + "skip-" + name
		if r := ih.deliver(t, "issues", d, payload); r.Status != 200 || r.Enqueued != 1 {
			t.Fatalf("%s: %+v, want 200 enqueued 1 (triage only)", name, r)
		}
		ih.wantQueued(t, d, map[string]int{"jobs": 1})
	}
}

func TestIngestRedeliveryEnqueuesOnce(t *testing.T) {
	ih := setupIngest(t)
	issue, pr := ih.prefix+"redeliver-issue", ih.prefix+"redeliver-pr"
	for i := 0; i < 3; i++ {
		want := 2
		if i > 0 {
			want = 0
		}
		if r := ih.deliver(t, "issues", issue, ih.issue("opened", 46, human, "")); r.Status != 200 || r.Enqueued != want {
			t.Fatalf("issue delivery %d: %+v, want 200 enqueued %d", i+1, r, want)
		}
		if r := ih.deliver(t, "pull_request", pr, ih.pull("opened", 87, nil)); r.Status != 200 || r.Enqueued != want/2 {
			t.Fatalf("pr delivery %d: %+v, want 200 enqueued %d", i+1, r, want/2)
		}
	}
	ih.wantQueued(t, issue, map[string]int{"jobs": 1, "jobs_issue": 1})
	ih.wantQueued(t, pr, map[string]int{"jobs_review": 1})
}

// "opened" then "ready_for_review" (or a re-sent synchronize) for the same
// head is one review job; a new head is a new job.
func TestIngestReviewDedupesPerHead(t *testing.T) {
	ih := setupIngest(t)
	first, sameHead, newHead := ih.prefix+"head-1", ih.prefix+"head-2", ih.prefix+"head-3"
	if r := ih.deliver(t, "pull_request", first, ih.pull("opened", 88, nil)); r.Enqueued != 1 {
		t.Fatalf("opened: %+v, want enqueued 1", r)
	}
	if r := ih.deliver(t, "pull_request", sameHead, ih.pull("ready_for_review", 88, nil)); r.Status != 200 || r.Enqueued != 0 {
		t.Fatalf("same head: %+v, want 200 enqueued 0", r)
	}
	moved := ih.pull("synchronize", 88, func(pr map[string]any) {
		pr["head"] = map[string]any{"sha": strings.Repeat("3", 40), "repo": map[string]any{"full_name": ih.repo}}
	})
	if r := ih.deliver(t, "pull_request", newHead, moved); r.Status != 200 || r.Enqueued != 1 {
		t.Fatalf("new head: %+v, want 200 enqueued 1", r)
	}
	ih.wantQueued(t, first, map[string]int{"jobs_review": 1})
	ih.wantQueued(t, sameHead, map[string]int{})
	ih.wantQueued(t, newHead, map[string]int{"jobs_review": 1})
}

func TestIngestRejectsBadSignature(t *testing.T) {
	ih := setupIngest(t)
	d := ih.prefix + "forged"
	body := []byte(mustJSON(t, ih.pull("opened", 89, nil)))
	mac := hmac.New(sha256.New, []byte("not-the-secret"))
	mac.Write(body)
	if r := ih.post(t, "pull_request", d, body, "sha256="+hex.EncodeToString(mac.Sum(nil))); r.Status != 401 {
		t.Fatalf("forged signature: %+v, want 401", r)
	}
	if r := ih.post(t, "pull_request", d, body, ""); r.Status != 401 {
		t.Fatalf("missing signature: %+v, want 401", r)
	}
	ih.wantQueued(t, d, map[string]int{})
}

// The SQL entry point is the boundary: only service_role may call it, only
// agent queues are routable, and a refused job rolls the delivery back.
func TestIngestRouteFunctionGuards(t *testing.T) {
	ih := setupIngest(t)
	for _, role := range []string{"anon", "authenticated", "tierbound_worker"} {
		var can bool
		if err := ih.db.QueryRow(t.Context(),
			`select has_function_privilege($1, 'public.tierbound_ingest_route(text, jsonb)', 'execute')`, role).Scan(&can); err != nil {
			t.Fatal(err)
		}
		if can {
			t.Fatalf("%s can execute tierbound_ingest_route", role)
		}
	}
	d := ih.prefix + "dead"
	jobs := `[{"queue":"jobs_dead","message":{"repo":"x/y","issue_number":1}}]`
	_, err := ih.db.Exec(t.Context(), `select public.tierbound_ingest_route($1, $2::jsonb)`, d, jobs)
	if err == nil || !strings.Contains(err.Error(), "not routable") {
		t.Fatalf("routing to jobs_dead: err = %v, want not routable", err)
	}
	var recorded bool
	if err := ih.db.QueryRow(t.Context(),
		`select exists (select 1 from public.webhook_deliveries where delivery_id = $1)`, d).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("a refused route still recorded the delivery; its redelivery would be dropped")
	}
}

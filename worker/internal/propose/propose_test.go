package propose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
	"github.com/MohamedBenTaher/tierbound/worker/internal/pgmq"
)

type fakeFetcher struct {
	issue githubro.Issue
}

func (f fakeFetcher) FetchIssue(_ context.Context, repo string, number int) (githubro.Issue, error) {
	issue := f.issue
	issue.Repo = repo
	issue.Number = number
	return issue, nil
}

type fakeDrafter struct {
	body string
}

func (f fakeDrafter) DraftComment(_ context.Context, _, _ string) (string, error) {
	return f.body, nil
}

const fakeDraftBody = "Summary: it breaks.\nLikely area: worker.\nSuggested label: bug"

// setup seeds one agent row (auth.users requires only id locally) and returns
// deps wired to fakes, the agent id, and a cleanup func. Skips without
// TEST_DATABASE_URL so `go test ./...` stays green in pre-commit.
func setup(t *testing.T, evalsPassed bool) (Deps, string, func()) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("test-agent-%d", time.Now().UnixNano())
	cfg := agentconfig.Config{
		Name:      name,
		Tier:      agentconfig.TierDraft,
		Model:     "test-model",
		Prompt:    "draft one comment",
		ToolGrant: []string{"github.read_issues"},
	}
	configHash, err := cfg.Hash()
	if err != nil {
		t.Fatal(err)
	}
	evalsPassedHash := "stale-hash"
	if evalsPassed {
		evalsPassedHash = configHash
	}
	var ownerID string
	if err := pool.QueryRow(ctx,
		`insert into auth.users (id) values (gen_random_uuid()) returning id::text`,
	).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	var agentID string
	if err := pool.QueryRow(ctx, `
		insert into public.agents (id, owner, name, tier, config_hash, evals_passed_hash)
		values (gen_random_uuid(), $1::uuid, $2, 'draft', $3, $4)
		returning id::text`,
		ownerID, name, configHash, evalsPassedHash,
	).Scan(&agentID); err != nil {
		t.Fatal(err)
	}

	deps := Deps{
		Pool:    pool,
		Queue:   pgmq.New(pool),
		Fetcher: fakeFetcher{issue: githubro.Issue{Title: "it breaks", Body: "steps", Labels: []string{"bug"}}},
		Drafter: fakeDrafter{body: fakeDraftBody},
		Config:  cfg,
	}

	cleanup := func() {
		_, _ = pool.Exec(ctx, `delete from public.approvals where action_id in
			(select id from public.proposed_actions where run_id in
				(select id from public.runs where agent_id = $1::uuid))`, agentID)
		_, _ = pool.Exec(ctx, `delete from public.effects where action_id in
			(select id from public.proposed_actions where run_id in
				(select id from public.runs where agent_id = $1::uuid))`, agentID)
		_, _ = pool.Exec(ctx, `delete from public.proposed_actions where run_id in
			(select id from public.runs where agent_id = $1::uuid)`, agentID)
		_, _ = pool.Exec(ctx, `delete from public.runs where agent_id = $1::uuid`, agentID)
		_, _ = pool.Exec(ctx, `delete from public.agents where id = $1::uuid`, agentID)
		_, _ = pool.Exec(ctx, `delete from auth.users where id = $1::uuid`, ownerID)
		_, _ = pool.Exec(ctx, `delete from pgmq.q_jobs where message->>'delivery_id' like 'test-%'`)
		_, _ = pool.Exec(ctx, `delete from pgmq.q_jobs_dead where message->>'delivery_id' like 'test-%'`)
		pool.Close()
	}
	return deps, agentID, cleanup
}

func enqueue(t *testing.T, d Deps, job Job) {
	t.Helper()
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Queue.Send(context.Background(), "jobs", body); err != nil {
		t.Fatal(err)
	}
}

func countActions(t *testing.T, d Deps, agentID string) int {
	t.Helper()
	var n int
	err := d.Pool.QueryRow(context.Background(), `
		select count(*) from public.proposed_actions pa
		join public.runs r on r.id = pa.run_id
		where r.agent_id = $1::uuid`, agentID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProcessOneProposesExactlyOneAction(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()

	enqueue(t, deps, Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 7, DeliveryID: "test-1"})

	processed, err := ProcessOne(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("queue was empty")
	}

	if n := countActions(t, deps, agentID); n != 1 {
		t.Fatalf("want 1 proposed_action, got %d", n)
	}

	wantHash, err := hashing.ActionHash(KindGitHubComment,
		map[string]any{"repo": "MohamedBenTaher/tierbound", "issue_number": 7},
		map[string]any{"body": fakeDraftBody})
	if err != nil {
		t.Fatal(err)
	}
	var gotHash, runStatus, key string
	err = deps.Pool.QueryRow(context.Background(), `
		select pa.payload_hash, r.status, pa.idempotency_key
		from public.proposed_actions pa
		join public.runs r on r.id = pa.run_id
		where r.agent_id = $1::uuid`, agentID).Scan(&gotHash, &runStatus, &key)
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("payload_hash is not the canonical payload hash\n got: %s\nwant: %s", gotHash, wantHash)
	}
	if runStatus != "succeeded" {
		t.Fatalf("run status = %q, want succeeded", runStatus)
	}
	wantKeySuffix := ":MohamedBenTaher/tierbound:7:"
	if !strings.Contains(key, wantKeySuffix) {
		t.Fatalf("idempotency_key %q does not follow agent:repo:issue:config_hash", key)
	}
}

func TestProcessOneDuplicateRetriesStayOneAction(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()

	job := Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 7, DeliveryID: "test-dup"}
	enqueue(t, deps, job)
	enqueue(t, deps, job)

	for i := 0; i < 2; i++ {
		processed, err := ProcessOne(context.Background(), deps)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if !processed {
			t.Fatalf("attempt %d: queue was empty", i+1)
		}
	}

	if n := countActions(t, deps, agentID); n != 1 {
		t.Fatalf("redelivery produced %d actions, want 1", n)
	}
}

func TestProcessOneRefusesWithoutEvalsPass(t *testing.T) {
	deps, agentID, cleanup := setup(t, false)
	defer cleanup()

	enqueue(t, deps, Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 7, DeliveryID: "test-refuse"})

	if _, err := ProcessOne(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	if n := countActions(t, deps, agentID); n != 0 {
		t.Fatalf("gate refused but %d actions were proposed", n)
	}
	var status string
	err := deps.Pool.QueryRow(context.Background(), `
		select status from public.runs where agent_id = $1::uuid`, agentID).Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	if status != "skipped" {
		t.Fatalf("run status = %q, want skipped", status)
	}
}

// Plan step 3.3: a worker running a config other than the registered (and
// gated) one must not propose, even though the row's hashes agree.
func TestProcessOneRefusesUnregisteredRunningConfig(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	deps.Config.Prompt += "\nAlso promise a fix date."

	enqueue(t, deps, Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 8, DeliveryID: "test-unregistered"})

	if _, err := ProcessOne(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	if n := countActions(t, deps, agentID); n != 0 {
		t.Fatalf("unregistered config proposed %d actions", n)
	}
}

func TestProcessOneDeadLettersWrongShapeJob(t *testing.T) {
	deps, _, cleanup := setup(t, true)
	defer cleanup()

	// Valid JSON (pgmq stores jsonb) that is not a Job: no repo/issue_number.
	// The delivery_id marker lets cleanup find it.
	const wrongShape = `{"delivery_id":"test-shape","unexpected":true}`
	if err := deps.Queue.Send(context.Background(), "jobs", []byte(wrongShape)); err != nil {
		t.Fatal(err)
	}
	processed, err := ProcessOne(context.Background(), deps)
	if !processed {
		t.Fatal("queue was empty")
	}
	if err == nil {
		t.Fatal("wrong-shape job did not report an error")
	}

	var queued, dead int
	if err := deps.Pool.QueryRow(context.Background(),
		`select count(*) from pgmq.q_jobs where message = $1::jsonb`, wrongShape).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := deps.Pool.QueryRow(context.Background(),
		`select count(*) from pgmq.q_jobs_dead where message = $1::jsonb`, wrongShape).Scan(&dead); err != nil {
		t.Fatal(err)
	}
	if queued != 0 || dead != 1 {
		t.Fatalf("wrong-shape job: q_jobs=%d q_jobs_dead=%d, want 0/1", queued, dead)
	}
}

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

const fixtureRepo = "tierbound-e2e/fixture"

type harness struct {
	db             *pgxpool.Pool
	supabaseURL    string
	anonKey        string
	serviceKey     string
	webhookSecret  string
	workerDBURL    string
	fakeAddr       string
	appKeyFile     string
	appID          string
	installationID string
}

var issueSeq atomic.Int64

// setup connects to the local stack. It skips unless scripts/test-effector.sh
// (or an equivalent environment) provides every variable, so `go test ./...`
// stays green in pre-commit.
func setup(t *testing.T) harness {
	t.Helper()
	vars := map[string]string{}
	for _, name := range []string{
		"TEST_DATABASE_URL", "TEST_WORKER_DATABASE_URL", "TEST_SUPABASE_URL",
		"TEST_SUPABASE_ANON_KEY", "TEST_SUPABASE_SERVICE_ROLE_KEY",
		"TEST_EFFECTOR_WEBHOOK_SECRET", "TEST_FAKE_GITHUB_ADDR", "TEST_GITHUB_APP_KEY_FILE",
		"TEST_GITHUB_APP_ID", "TEST_GITHUB_APP_INSTALLATION_ID",
	} {
		vars[name] = os.Getenv(name)
		if vars[name] == "" {
			t.Skipf("%s not set; run scripts/test-effector.sh", name)
		}
	}
	pool, err := pgxpool.New(context.Background(), vars["TEST_DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return harness{
		db:             pool,
		supabaseURL:    strings.TrimRight(vars["TEST_SUPABASE_URL"], "/"),
		anonKey:        vars["TEST_SUPABASE_ANON_KEY"],
		serviceKey:     vars["TEST_SUPABASE_SERVICE_ROLE_KEY"],
		webhookSecret:  vars["TEST_EFFECTOR_WEBHOOK_SECRET"],
		workerDBURL:    vars["TEST_WORKER_DATABASE_URL"],
		fakeAddr:       vars["TEST_FAKE_GITHUB_ADDR"],
		appKeyFile:     vars["TEST_GITHUB_APP_KEY_FILE"],
		appID:          vars["TEST_GITHUB_APP_ID"],
		installationID: vars["TEST_GITHUB_APP_INSTALLATION_ID"],
	}
}

func (h harness) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Fields(sql)[0], err)
	}
}

// action is one seeded proposed_action and where its comment would land.
type action struct {
	ID    string
	Hash  string
	Body  string
	Issue string // fake GitHub issue key, owner/repo#number
	Owner string
}

func (a action) wantComment() string {
	return a.Body + "\n\n<!-- tierbound:" + a.ID + " -->"
}

// seedUser inserts a bare auth.users row (enough for owner foreign keys).
func (h harness) seedUser(t *testing.T) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(),
		`insert into auth.users (id) values (gen_random_uuid()) returning id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.cleanupOwner(id) })
	return id
}

// seedAction creates agent -> run -> proposed_action for owner, with the
// payload_hash the worker would write (hashing.ActionHash, plan step 1.4).
func (h harness) seedAction(t *testing.T, owner string) action {
	t.Helper()
	issue := int(time.Now().Unix()%100000)*100 + int(issueSeq.Add(1)%100)
	body := fmt.Sprintf("Summary: e2e fixture %d.\nLikely area: effector.\nSuggested label: test", issue)
	target := map[string]any{"repo": fixtureRepo, "issue_number": issue}
	payload := map[string]any{"body": body}
	hash, err := hashing.ActionHash("github_comment", target, payload)
	if err != nil {
		t.Fatal(err)
	}
	id := h.insertAction(t, owner, "github_comment", mustJSON(t, target), mustJSON(t, payload), hash)
	return action{ID: id, Hash: hash, Body: body, Issue: fmt.Sprintf("%s#%d", fixtureRepo, issue), Owner: owner}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// insertAction inserts one action of kind (with its own draft agent and run)
// from raw JSON target/payload and returns its id.
func (h harness) insertAction(t *testing.T, owner, kind, targetJSON, payloadJSON, hash string) string {
	t.Helper()
	var id string
	err := h.db.QueryRow(context.Background(), `
		with agent as (
			insert into public.agents (owner, name, tier, config_hash, evals_passed_hash)
			values ($1::uuid, 'e2e-' || gen_random_uuid(), 'draft', 'e2e', 'e2e')
			returning id),
		run as (
			insert into public.runs (agent_id, attempt, status)
			select id, 1, 'succeeded' from agent returning id)
		insert into public.proposed_actions (run_id, kind, target, payload, payload_hash, idempotency_key)
		select run.id, $5, $2::jsonb, $3::jsonb, $4, 'e2e:' || gen_random_uuid()
		from run returning id::text`,
		owner, targetJSON, payloadJSON, hash, kind).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (h harness) cleanupOwner(owner string) {
	ctx := context.Background()
	scope := `(select pa.id from public.proposed_actions pa join public.runs r on r.id = pa.run_id
		join public.agents a on a.id = r.agent_id where a.owner = $1::uuid)`
	for _, sql := range []string{
		`delete from public.effects where action_id in ` + scope,
		`delete from public.approvals where action_id in ` + scope,
		`delete from public.proposed_actions where id in ` + scope,
		`delete from public.runs where agent_id in (select id from public.agents where owner = $1::uuid)`,
		`delete from public.agents where owner = $1::uuid`,
		`delete from auth.users where id = $1::uuid`,
	} {
		_, _ = h.db.Exec(ctx, sql, owner)
	}
}

// forceApprove inserts an approvals row directly in SQL (as the database
// owner), which fires the effector webhook exactly like the MCP insert does.
func (h harness) forceApprove(t *testing.T, a action, hash string) {
	t.Helper()
	h.exec(t, `insert into public.approvals (action_id, approver, payload_hash, decision)
		values ($1::uuid, $2::uuid, $3, 'approved')`, a.ID, a.Owner, hash)
}

type effectRow struct {
	Exists     bool
	Status     string
	ExternalID *string
	Attempts   int
}

func (h harness) effect(t *testing.T, a action) effectRow {
	t.Helper()
	var e effectRow
	err := h.db.QueryRow(context.Background(), `
		select true, status::text, external_id, attempts from public.effects where action_id = $1::uuid`,
		a.ID).Scan(&e.Exists, &e.Status, &e.ExternalID, &e.Attempts)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		t.Fatal(err)
	}
	return e
}

func (h harness) actionStatus(t *testing.T, a action) string {
	t.Helper()
	var s string
	if err := h.db.QueryRow(context.Background(),
		`select status::text from public.proposed_actions where id = $1::uuid`, a.ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// webhookResponses returns the HTTP statuses pg_net recorded for effector
// calls about this action (the function echoes the action id in its body).
func (h harness) webhookResponses(t *testing.T, a action) []int {
	t.Helper()
	rows, err := h.db.Query(context.Background(),
		`select status_code from net._http_response where content like '%' || $1 || '%' order by id`, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var code *int
		if err := rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		if code != nil {
			out = append(out, *code)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

type invokeResult struct {
	Status  int
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
}

// invoke calls the effector directly, as a replayed webhook would.
func (h harness) invoke(t *testing.T, a action) invokeResult {
	t.Helper()
	res, err := h.tryInvoke(a)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// tryInvoke is invoke for goroutines (no t.Fatal off the test goroutine).
func (h harness) tryInvoke(a action) (invokeResult, error) {
	body, err := json.Marshal(map[string]string{"action_id": a.ID})
	if err != nil {
		return invokeResult{}, err
	}
	headers := map[string]string{"x-tierbound-webhook-secret": h.webhookSecret}
	status, raw, err := h.request(http.MethodPost, "/functions/v1/effect-github-comment", headers, body)
	if err != nil {
		return invokeResult{}, err
	}
	var res invokeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return invokeResult{}, fmt.Errorf("effector returned %d %q: %w", status, raw, err)
	}
	res.Status = status
	return res, nil
}

func (h harness) request(method, path string, headers map[string]string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, h.supabaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

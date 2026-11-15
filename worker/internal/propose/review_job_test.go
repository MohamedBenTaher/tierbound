package propose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
)

const (
	testHead  = "3f2a9c0b7d1e4f5a6b7c8d9e0f1a2b3c4d5e6f70"
	testHead2 = "4f2a9c0b7d1e4f5a6b7c8d9e0f1a2b3c4d5e6f71"
	testBase  = "1111111111111111111111111111111111111111"
)

const testAgents = "# AGENTS.md\n\n## Invariants (never break these)\n\n" +
	"1. **The worker never holds write credentials or the GitHub App key.** It reads GitHub with read-only tokens only.\n"

// fakePRs serves one PR's files and rule files, recording the refs asked for.
type fakePRs struct {
	files []githubro.PRFile
	rules map[string]string
	refs  *[]string
}

func (f fakePRs) FetchPRFiles(_ context.Context, _ string, _ int) ([]githubro.PRFile, error) {
	return f.files, nil
}

func (f fakePRs) FetchFile(_ context.Context, _, path, ref string) (string, error) {
	*f.refs = append(*f.refs, path+"@"+ref)
	text, ok := f.rules[path]
	if !ok {
		return "", githubro.ErrNotFound
	}
	return text, nil
}

// reviewAgent turns the setup agent into review-pr on jobs_review, with
// AGENTS.md invariants-1 as the one blocking rule, registered and gated.
func reviewAgent(t *testing.T, deps *Deps, agentID string) *[]string {
	t.Helper()
	deps.Config.ToolGrant = []string{"github.read_pulls", agentconfig.GrantProposeReview}
	deps.Config.Queue = "jobs_review"
	deps.Config.Review = agentconfig.Review{
		RuleSources:   []string{"AGENTS.md", "docs/review-rules.md"},
		BlockingRules: []string{"AGENTS.md#invariants-1"},
		Status:        true,
	}
	hash, err := deps.Config.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(context.Background(), `update public.agents set config_hash = $2, evals_passed_hash = $2
		where id = $1::uuid`, agentID, hash); err != nil {
		t.Fatal(err)
	}
	refs := &[]string{}
	deps.PRs = fakePRs{
		files: []githubro.PRFile{{Path: "supabase/migrations/x.sql", Patch: "@@ -0,0 +1,2 @@\n+create table t();\n+grant insert on public.approvals to tierbound_worker;"}},
		rules: map[string]string{"AGENTS.md": testAgents},
		refs:  refs,
	}
	// One grounded blocker on the blocking rule, one finding on a line that
	// is not in the diff.
	deps.Drafter = fakeDrafter{body: `{"summary":"One least-privilege risk.","findings":[
		{"path":"supabase/migrations/x.sql","line":2,"side":"RIGHT","severity":"blocker",
		 "rule":{"source":"AGENTS.md","anchor":"invariants-1","quote":"The worker never holds write credentials or the GitHub App key."},
		 "evidence":[],"message":"Grants the worker a write on approvals.","suggestion":"Drop the grant."},
		{"path":"supabase/migrations/x.sql","line":40,"side":"RIGHT","severity":"major",
		 "rule":{"source":"AGENTS.md","anchor":"invariants-1","quote":"The worker never holds write credentials or the GitHub App key."},
		 "evidence":[],"message":"Invented line.","suggestion":""}]}`}
	return refs
}

func sendReview(t *testing.T, d Deps, head string) {
	t.Helper()
	body, _ := json.Marshal(Job{Repo: "o/r", PRNumber: 12, HeadSha: head, BaseSha: testBase, DeliveryID: "test-review"})
	if err := d.Queue.Send(context.Background(), "jobs_review", body); err != nil {
		t.Fatal(err)
	}
	if processed, err := ProcessOne(context.Background(), d); err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
}

type reviewRow struct {
	Event     string
	Comments  int
	State     string
	Hash, SQL string
	Stats     map[string]any
}

func lastReview(t *testing.T, d Deps, agentID, head string) reviewRow {
	t.Helper()
	var r reviewRow
	var stats []byte
	if err := d.Pool.QueryRow(context.Background(), `
		select pa.payload->>'event', jsonb_array_length(pa.payload->'comments'), coalesce(pa.payload#>>'{status,state}', ''),
			pa.payload_hash, public.tierbound_action_hash(pa.kind, pa.target, pa.payload), r.stats
		from public.proposed_actions pa join public.runs r on r.id = pa.run_id
		where r.agent_id = $1::uuid and pa.kind = 'github_pr_review' and pa.target->>'head_sha' = $2`,
		agentID, head).Scan(&r.Event, &r.Comments, &r.State, &r.Hash, &r.SQL, &stats); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(stats, &r.Stats)
	return r
}

func cleanReviewQueue(d Deps) {
	_, _ = d.Pool.Exec(context.Background(), `delete from pgmq.q_jobs_review where message->>'delivery_id' like 'test-%'`)
}

// Plan Phase 5 / V2_ACTIONS 4.2: a review job proposes one github_pr_review
// per head sha. The ungrounded finding is dropped and counted in
// runs.stats; rules are read at base_sha; without the block capability the
// grounded blocker still only comments.
func TestReviewAgentProposesGroundedReview(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	defer cleanReviewQueue(deps)
	refs := reviewAgent(t, &deps, agentID)
	var logs []string
	deps.Log = func(line string) { logs = append(logs, line) }

	sendReview(t, deps, testHead)
	sendReview(t, deps, testHead) // redelivery of the same head: still one action
	if n := countActions(t, deps, agentID); n != 1 {
		t.Fatalf("same head twice: %d actions, want 1", n)
	}
	r := lastReview(t, deps, agentID, testHead)
	if r.Event != "COMMENT" || r.State != "success" || r.Comments != 1 || r.Hash != r.SQL {
		t.Fatalf("review = %+v, want COMMENT/success with 1 comment and matching hashes", r)
	}
	if r.Stats["ungrounded"] != float64(1) || r.Stats["findings_proposed"] != float64(2) {
		t.Fatalf("runs.stats = %v, want 2 proposed, 1 ungrounded", r.Stats)
	}
	for _, ref := range *refs {
		if !strings.HasSuffix(ref, "@"+testBase) {
			t.Fatalf("rule file read at %s, want base sha", ref)
		}
	}
	want := "proposed github_pr_review "
	if len(logs) != 2 || !strings.HasPrefix(logs[0], want) ||
		!strings.HasSuffix(logs[0], "for o/r#12 (1 findings kept, 1 ungrounded dropped, COMMENT)") {
		t.Fatalf("logs = %q", logs)
	}

	sendReview(t, deps, testHead2) // a new push: a new review
	if n := countActions(t, deps, agentID); n != 2 {
		t.Fatalf("new head: %d actions, want 2", n)
	}
}

// The block mechanism exists but is inactive until a capability is granted
// for the agent's current config_hash; then the same findings request
// changes and the insert trigger admits it.
func TestReviewBlocksOnlyWithCapability(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	defer cleanReviewQueue(deps)
	reviewAgent(t, &deps, agentID)
	if _, err := deps.Pool.Exec(context.Background(), `insert into public.agent_capabilities (agent_id, capability, granted_hash)
		select id, 'block', config_hash from public.agents where id = $1::uuid`, agentID); err != nil {
		t.Fatal(err)
	}
	sendReview(t, deps, testHead)
	if r := lastReview(t, deps, agentID, testHead); r.Event != "REQUEST_CHANGES" || r.State != "failure" {
		t.Fatalf("with capability: %+v, want REQUEST_CHANGES/failure", r)
	}
}

func TestValidateReviewJob(t *testing.T) {
	ok := Job{Repo: "o/r", PRNumber: 1, HeadSha: testHead, BaseSha: testBase}
	if err := validateJob(KindGitHubPRReview, ok); err != nil {
		t.Fatal(err)
	}
	for name, j := range map[string]Job{
		"no pr":       {Repo: "o/r", HeadSha: testHead, BaseSha: testBase},
		"short head":  {Repo: "o/r", PRNumber: 1, HeadSha: "abc", BaseSha: testBase},
		"no base":     {Repo: "o/r", PRNumber: 1, HeadSha: testHead},
		"issue shape": {Repo: "o/r", IssueNumber: 3},
	} {
		if validateJob(KindGitHubPRReview, j) == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

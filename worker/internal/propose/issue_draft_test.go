package propose

import (
	"context"
	"strings"
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// TestIssueAgentProposesOneIssue is plan step 4.6: an agent whose tool_grant
// has github.propose_issue reads its own queue and proposes exactly one
// github_issue action (target {repo}), hashed like the effector recomputes.
func TestIssueAgentProposesOneIssue(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	ctx := context.Background()

	issueAgent(t, &deps, agentID)
	deps.Drafter = fakeDrafter{body: `{"title":"Retry the effector once","body":"## Problem\nIt retries forever.\n\n## Done when\n- [ ] one retry","labels":["bug"]}`}
	defer func() {
		_, _ = deps.Pool.Exec(ctx, `delete from pgmq.q_jobs_issue where message->>'delivery_id' like 'test-%'`)
	}()

	job := Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 9, DeliveryID: "test-issue"}
	for i := 0; i < 2; i++ { // the same job twice: still one action
		body := []byte(`{"repo":"MohamedBenTaher/tierbound","issue_number":9,"delivery_id":"test-issue"}`)
		if err := deps.Queue.Send(ctx, "jobs_issue", body); err != nil {
			t.Fatal(err)
		}
		if processed, err := ProcessOne(ctx, deps); err != nil || !processed {
			t.Fatalf("attempt %d: processed=%v err=%v", i+1, processed, err)
		}
	}
	if n := countActions(t, deps, agentID); n != 1 {
		t.Fatalf("want 1 proposed_action, got %d", n)
	}

	want, err := hashing.ActionHash(KindGitHubIssue, map[string]any{"repo": job.Repo}, map[string]any{
		"title": "Retry the effector once", "body": "## Problem\nIt retries forever.\n\n## Done when\n- [ ] one retry",
		"labels": []any{"bug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kind, gotHash, recomputed string
	if err := deps.Pool.QueryRow(ctx, `
		select pa.kind, pa.payload_hash, public.tierbound_action_hash(pa.kind, pa.target, pa.payload)
		from public.proposed_actions pa join public.runs r on r.id = pa.run_id
		where r.agent_id = $1::uuid`, agentID).Scan(&kind, &gotHash, &recomputed); err != nil {
		t.Fatal(err)
	}
	if kind != KindGitHubIssue || gotHash != want || recomputed != want {
		t.Fatalf("got kind %s hash %s (SQL %s), want github_issue %s", kind, gotHash, recomputed, want)
	}
}

// issueAgent turns the setup agent into an issue-proposing agent on the
// jobs_issue queue, registered and gated for its new config_hash.
func issueAgent(t *testing.T, deps *Deps, agentID string) {
	t.Helper()
	ctx := context.Background()
	deps.Config.ToolGrant = []string{"github.read_issues", agentconfig.GrantProposeIssue}
	deps.Config.Queue = "jobs_issue"
	hash, err := deps.Config.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `update public.agents set config_hash = $2, evals_passed_hash = $2
		where id = $1::uuid`, agentID, hash); err != nil {
		t.Fatal(err)
	}
}

// Plan step 4.7: an abstain reply proposes nothing, records the run as
// skipped with the reason, and archives the job instead of retrying it.
func TestIssueAgentAbstainProposesNothing(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	ctx := context.Background()
	issueAgent(t, &deps, agentID)
	deps.Drafter = fakeDrafter{body: `{"abstain":"no actionable problem in the input"}`}

	body := []byte(`{"repo":"MohamedBenTaher/tierbound","issue_number":10,"delivery_id":"test-abstain"}`)
	if err := deps.Queue.Send(ctx, "jobs_issue", body); err != nil {
		t.Fatal(err)
	}
	if processed, err := ProcessOne(ctx, deps); err != nil || !processed {
		t.Fatalf("processed=%v err=%v, want processed without error", processed, err)
	}
	if n := countActions(t, deps, agentID); n != 0 {
		t.Fatalf("abstain proposed %d actions, want 0", n)
	}
	var status, reason string
	if err := deps.Pool.QueryRow(ctx, `select status, coalesce(error, '') from public.runs
		where agent_id = $1::uuid`, agentID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "skipped" || !strings.Contains(reason, "no actionable problem") {
		t.Fatalf("run = %s %q, want skipped with the abstain reason", status, reason)
	}
	var left int
	if err := deps.Pool.QueryRow(ctx, `select count(*) from pgmq.q_jobs_issue
		where message->>'delivery_id' = 'test-abstain'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("abstained job still queued (%d), want archived", left)
	}
}

// A comment agent ignores the issue queue: kinds and queues come from config.
func TestKindForFollowsToolGrant(t *testing.T) {
	c := agentconfig.Config{ToolGrant: []string{"github.read_issues"}}
	if k := kindFor(c); k != KindGitHubComment || c.QueueName() != "jobs" {
		t.Fatalf("triage config -> %s on %s, want github_comment on jobs", k, c.QueueName())
	}
	c.ToolGrant = append(c.ToolGrant, agentconfig.GrantProposeIssue)
	if k := kindFor(c); k != KindGitHubIssue {
		t.Fatalf("propose_issue grant -> %s, want github_issue", k)
	}
}

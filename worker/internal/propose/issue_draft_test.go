package propose

import (
	"context"
	"strings"
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

func TestParseIssueDraft(t *testing.T) {
	long := strings.Repeat("x", maxIssueTitle+1)
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"valid", `{"title":"Fix retry","body":"## Problem\nx","labels":["bug"]}`, true},
		{"valid, no labels key", `{"title":"Fix retry","body":"b"}`, true},
		{"markdown fence", "```json\n{\"title\":\"t\",\"body\":\"b\",\"labels\":[]}\n```", false},
		{"preamble", `Here you go: {"title":"t","body":"b","labels":[]}`, false},
		{"unknown field", `{"title":"t","body":"b","labels":[],"assignee":"x"}`, false},
		{"trailing object", `{"title":"t","body":"b","labels":[]} {}`, false},
		{"title too long", `{"title":"` + long + `","body":"b","labels":[]}`, false},
		{"multi-line title", `{"title":"a\nb","body":"b","labels":[]}`, false},
		{"empty body", `{"title":"t","body":"  ","labels":[]}`, false},
		{"empty label", `{"title":"t","body":"b","labels":[" "]}`, false},
		{"eleven labels", `{"title":"t","body":"b","labels":["1","2","3","4","5","6","7","8","9","10","11"]}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseIssueDraft(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("ParseIssueDraft(%q) err = %v, want ok=%v", c.in, err, c.ok)
			}
		})
	}
}

// TestIssueAgentProposesOneIssue is plan step 4.6: an agent whose tool_grant
// has github.propose_issue reads its own queue and proposes exactly one
// github_issue action (target {repo}), hashed like the effector recomputes.
func TestIssueAgentProposesOneIssue(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
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

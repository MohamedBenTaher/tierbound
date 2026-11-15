package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// Plan step 4.4: the MCP surface shows a github_issue as a new issue (repo,
// title, labels, body), the kind filter finds it, and approving its exact
// hash over MCP opens exactly one issue.
func TestMCPShowsAndApprovesIssue(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)
	a := h.seedIssueAction(t, owner, []string{"bug"})

	r := h.callTool(t, token, "get_action", map[string]any{"action_id": a.ID})
	if r.IsError {
		t.Fatalf("get_action: %s", r.Text)
	}
	var got struct {
		Kind           string `json:"kind"`
		Preview        string `json:"preview"`
		RecomputedHash string `json:"recomputed_hash"`
	}
	if err := json.Unmarshal([]byte(r.Text), &got); err != nil {
		t.Fatal(err)
	}
	wantPreview := "New issue in " + a.Issue + "\nTitle: Effector retries forever\nLabels: bug\n\n" + a.Body
	if got.Kind != "github_issue" || got.Preview != wantPreview || got.RecomputedHash != a.Hash {
		t.Fatalf("get_action = %+v\nwant kind github_issue, preview %q, hash %s", got, wantPreview, a.Hash)
	}

	list := h.callTool(t, token, "list_pending_actions", map[string]any{"kind": "github_issue"})
	if list.IsError || !strings.Contains(list.Text, a.ID) || !strings.Contains(list.Text, "new issue in "+a.Issue) {
		t.Fatalf("list_pending_actions(kind=github_issue) = %s, want %s with its summary", list.Text, a.ID)
	}
	if other := h.callTool(t, token, "list_pending_actions", map[string]any{"kind": "github_comment"}); strings.Contains(other.Text, a.ID) {
		t.Fatalf("kind=github_comment listed the issue action %s", a.ID)
	}

	if r := h.callTool(t, token, "approve_action", map[string]any{"action_id": a.ID, "payload_hash": a.Hash}); r.IsError {
		t.Fatalf("approve_action: %s", r.Text)
	}
	h.waitPosted(t, a)
	assertOneIssue(t, gh, a, 1)
}

// Plan step 5.4: get_action previews a review with its verdict line, the
// quoted rule behind each finding, every inline comment, and the head sha.
func TestMCPPreviewsReview(t *testing.T) {
	h := setup(t)
	owner, token := h.signedInUser(t)
	target := map[string]any{"repo": "o/r", "pr_number": 12, "head_sha": reviewHead}
	payload := map[string]any{
		"event": "COMMENT", "body": "tierbound review: 1 finding.",
		"comments": []any{map[string]any{"path": "a.sql", "line": 3, "side": "RIGHT", "body": "Grants a write on approvals."}},
		"status":   map[string]any{"state": "success", "context": "tierbound/review", "description": "advisory"},
		"findings": []any{map[string]any{
			"path": "a.sql", "line": 3, "side": "RIGHT", "severity": "blocker",
			"rule":     map[string]any{"source": "AGENTS.md", "anchor": "invariants-1", "quote": "The worker never holds write credentials"},
			"evidence": []any{}, "message": "Grants a write on approvals.", "suggestion": "Drop it.",
		}},
	}
	hash, err := hashing.ActionHash("github_pr_review", target, payload)
	if err != nil {
		t.Fatal(err)
	}
	id := h.insertAction(t, owner, "github_pr_review", mustJSON(t, target), mustJSON(t, payload), hash)

	r := h.callTool(t, token, "get_action", map[string]any{"action_id": id})
	var got struct {
		Preview string `json:"preview"`
	}
	if err := json.Unmarshal([]byte(r.Text), &got); r.IsError || err != nil {
		t.Fatalf("get_action: %s %v", r.Text, err)
	}
	for _, want := range []string{
		"Review of o/r#12 at 3f2a9c0", "COMMENT · status success (advisory)",
		`a.sql:3 [blocker] AGENTS.md#invariants-1: "The worker never holds write credentials"`,
		"--- a.sql:3 (RIGHT)\nGrants a write on approvals.", "/pull/12/files/" + reviewHead,
	} {
		if !strings.Contains(got.Preview, want) {
			t.Fatalf("preview lacks %q:\n%s", want, got.Preview)
		}
	}
}

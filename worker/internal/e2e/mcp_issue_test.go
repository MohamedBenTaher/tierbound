package e2e

import (
	"encoding/json"
	"strings"
	"testing"
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

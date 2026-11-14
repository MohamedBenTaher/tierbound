package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// Plan step 4.3: an approved github_issue opens exactly one issue, carrying
// the marker, however often the effector runs.

var repoSeq = time.Now().UnixNano()

// seedIssueAction creates a github_issue action on a fresh fake repo, so the
// per-repo issue count belongs to this test alone.
func (h harness) seedIssueAction(t *testing.T, owner string, labels []string) action {
	t.Helper()
	repoSeq++
	repo := fmt.Sprintf("tierbound-e2e/issues-%d", repoSeq)
	body := "## Problem\nThe effector retried forever.\n\n## Done when\n- [ ] one retry"
	target := map[string]any{"repo": repo}
	ls := make([]any, len(labels))
	for i, l := range labels {
		ls[i] = l
	}
	payload := map[string]any{"title": "Effector retries forever", "body": body, "labels": ls}
	hash, err := hashing.ActionHash("github_issue", target, payload)
	if err != nil {
		t.Fatal(err)
	}
	id := h.insertAction(t, owner, "github_issue", mustJSON(t, target), mustJSON(t, payload), hash)
	return action{ID: id, Hash: hash, Body: body, Issue: repo, Owner: owner}
}

func assertOneIssue(t *testing.T, gh *fakeGitHub, a action, wantPosts int) fakeIssue {
	t.Helper()
	posts, issues := gh.issuesIn(a.Issue)
	if len(issues) != 1 || posts != wantPosts {
		t.Fatalf("fake GitHub has %d issues (%d POSTs) in %s, want 1 issue (%d POSTs)", len(issues), posts, a.Issue, wantPosts)
	}
	if issues[0].Body != a.wantComment() || issues[0].Title != "Effector retries forever" {
		t.Fatalf("issue = %q / %q, want the approved title and body plus marker", issues[0].Title, issues[0].Body)
	}
	return issues[0]
}

func TestIssueCreatedExactlyOnce(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	a := h.seedIssueAction(t, h.seedUser(t), []string{"bug", "effector"})

	h.forceApprove(t, a, a.Hash)

	e := h.waitPosted(t, a)
	issue := assertOneIssue(t, gh, a, 1)
	if e.ExternalID == nil || *e.ExternalID != issue.URL {
		t.Fatalf("effects.external_id = %v, want the issue URL %s", e.ExternalID, issue.URL)
	}
	if strings.Join(issue.Labels, ",") != "bug,effector" {
		t.Fatalf("labels = %v, want the approved [bug effector]", issue.Labels)
	}
	if s := h.actionStatus(t, a); s != "executed" {
		t.Fatalf("proposed_actions.status = %s, want executed", s)
	}
	for i := 0; i < 2; i++ {
		if r := h.invoke(t, a); r.Status != 200 || r.Outcome != "already_posted" {
			t.Fatalf("replay %d: %d %s %s, want 200 already_posted", i+1, r.Status, r.Outcome, r.Error)
		}
	}
	assertOneIssue(t, gh, a, 1)
}

// SPEC section 7 for issues: GitHub creates the issue but the response is
// lost. The retry lists issues since the approval, finds the marker and
// records the existing issue instead of opening a second one.
func TestIssueTimeoutAfterSuccessRecordsExisting(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.hangAfterStore = 4 * time.Second }) // > GITHUB_TIMEOUT_MS
	a := h.seedIssueAction(t, h.seedUser(t), []string{"bug"})

	h.forceApprove(t, a, a.Hash)
	waitFor(t, "effects.status = failed after the GitHub timeout", 20*time.Second, func() bool {
		return h.effect(t, a).Status == "failed"
	})

	gh.set(func(f *fakeGitHub) { f.hangAfterStore = 0 })
	if r := h.invoke(t, a); r.Status != 200 || r.Outcome != "recorded_existing" {
		t.Fatalf("retry: %d %s %s, want 200 recorded_existing", r.Status, r.Outcome, r.Error)
	}
	issue := assertOneIssue(t, gh, a, 1)
	if e := h.effect(t, a); e.Status != "posted" || e.ExternalID == nil || *e.ExternalID != issue.URL {
		t.Fatalf("after retry effects = %+v, want posted with %s", e, issue.URL)
	}
}

// V2_ACTIONS 4.1: the effector never creates labels. A label the repo does
// not have refuses the action, records why, and opens nothing.
func TestUnknownLabelRefused(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	a := h.seedIssueAction(t, h.seedUser(t), []string{"bug", "wontfix-typo"})

	h.forceApprove(t, a, a.Hash)
	waitFor(t, "effects.status = failed", 20*time.Second, func() bool {
		return h.effect(t, a).Status == "failed"
	})
	if r := h.invoke(t, a); r.Status != 403 || r.Outcome != "refused" || !strings.Contains(r.Error, "unknown_label: wontfix-typo") {
		t.Fatalf("invoke: %d %s %q, want 403 refused unknown_label: wontfix-typo", r.Status, r.Outcome, r.Error)
	}
	if posts, issues := gh.issuesIn(a.Issue); posts != 0 || len(issues) != 0 {
		t.Fatalf("fake GitHub got %d issue POSTs / %d issues, want none", posts, len(issues))
	}
	if s := h.actionStatus(t, a); s == "executed" {
		t.Fatal("proposed_actions.status = executed for a refused action")
	}
	if e := h.effect(t, a); e.ExternalID != nil {
		t.Fatalf("effects.external_id = %s for a refused action, want null", *e.ExternalID)
	}
}

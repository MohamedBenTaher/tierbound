package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Plan step 3.4 / postmortem-001: GitHub answers 500 to the create-comment
// call after the effector has claimed the action. The claim must end as
// 'failed' with no external_id (nothing was posted, nothing is recorded as
// posted), and a later retry must post exactly once.
func TestInjectedGitHub500ThenRetryPostsOnce(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.failPosts = true })
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash)
	waitFor(t, "effects.status = failed after the injected 500", 20*time.Second, func() bool {
		return h.effect(t, a).Status == "failed"
	})
	e := h.effect(t, a)
	if e.ExternalID != nil || e.Attempts != 1 {
		t.Fatalf("after the 500: effects = %+v, want failed, external_id null, attempts 1", e)
	}
	var lastError string
	if err := h.db.QueryRow(t.Context(),
		`select coalesce(last_error, '') from public.effects where action_id = $1::uuid`, a.ID).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError == "" {
		t.Fatal("last_error is empty after the injected 500; the operator would have nothing to go on")
	}
	// forceApprove skips decide_action's status update, so only "not
	// executed" is meaningful here.
	if s := h.actionStatus(t, a); s == "executed" {
		t.Fatalf("proposed_actions.status = executed after the 500")
	}
	assertNoComment(t, gh, a)

	// GitHub recovers; the operator retries (runbooks/effect-stuck.md).
	gh.set(func(f *fakeGitHub) { f.failPosts = false })
	if r := h.invoke(t, a); r.Status != http.StatusOK || r.Outcome != "posted" {
		t.Fatalf("retry: %d %s %s, want 200 posted", r.Status, r.Outcome, r.Error)
	}
	// Two POSTs total: the failed one and the retry. One comment.
	c := assertOneComment(t, gh, a, 2)
	e = h.effect(t, a)
	if e.Status != "posted" || e.ExternalID == nil || *e.ExternalID != c.URL || e.Attempts != 2 {
		t.Fatalf("after retry effects = %+v, want posted, %s, attempts 2", e, c.URL)
	}
	if s := h.actionStatus(t, a); s != "executed" {
		t.Fatalf("proposed_actions.status = %s, want executed", s)
	}

	// A second retry after success changes nothing.
	if r := h.invoke(t, a); r.Status != http.StatusOK || r.Outcome != "already_posted" {
		t.Fatalf("retry after success: %d %s %s, want 200 already_posted", r.Status, r.Outcome, r.Error)
	}
	assertOneComment(t, gh, a, 2)
}

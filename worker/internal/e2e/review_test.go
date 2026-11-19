package e2e

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// Plan step 5.4: an approved github_pr_review posts exactly one review,
// pinned to the approved head sha, plus its advisory status; a moved head
// writes nothing and marks the action stale.

const reviewHead = "3f2a9c0b7d1e4f5a6b7c8d9e0f1a2b3c4d5e6f70"

type reviewAction struct {
	action
	PR   string // owner/repo#number
	Repo string
}

func (h harness) seedReviewAction(t *testing.T, owner string) reviewAction {
	t.Helper()
	repoSeq++
	repo := fmt.Sprintf("tierbound-e2e/reviews-%d", repoSeq)
	target := map[string]any{"repo": repo, "pr_number": 12, "head_sha": reviewHead}
	body := "tierbound review: 1 finding."
	payload := map[string]any{
		"event": "COMMENT",
		"body":  body,
		"comments": []any{map[string]any{
			"path": "supabase/migrations/x.sql", "line": 3, "side": "RIGHT", "body": "Grants the worker a write on approvals.",
		}},
		"status":   map[string]any{"state": "success", "context": "tierbound/review", "description": "1 finding, advisory"},
		"findings": []any{},
	}
	hash, err := hashing.ActionHash("github_pr_review", target, payload)
	if err != nil {
		t.Fatal(err)
	}
	id := h.insertAction(t, owner, "github_pr_review", mustJSON(t, target), mustJSON(t, payload), hash)
	return reviewAction{action: action{ID: id, Hash: hash, Body: body, Issue: repo, Owner: owner}, PR: repo + "#12", Repo: repo}
}

func assertOneReview(t *testing.T, gh *fakeGitHub, a reviewAction, wantPosts int) fakeReview {
	t.Helper()
	posts, reviews, statuses := gh.reviewsOn(a.PR, a.Repo)
	if len(reviews) != 1 || posts != wantPosts {
		t.Fatalf("fake GitHub has %d reviews (%d POSTs) on %s, want 1 (%d POSTs)", len(reviews), posts, a.PR, wantPosts)
	}
	rv := reviews[0]
	if rv.CommitID != reviewHead || rv.Event != "COMMENT" || rv.Body != a.wantComment() || len(rv.Comments) != 1 {
		t.Fatalf("review = %+v, want COMMENT pinned to %s with the marker and 1 comment", rv, reviewHead)
	}
	last := statuses[len(statuses)-1]
	if last.SHA != reviewHead || last.State != "success" || last.Context != "tierbound/review" {
		t.Fatalf("latest status = %+v, want success tierbound/review on %s", last, reviewHead)
	}
	return rv
}

func TestReviewPostedExactlyOnceUnderConcurrency(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.postDelay = 1500 * time.Millisecond })
	a := h.seedReviewAction(t, h.seedUser(t))
	gh.setHead(a.PR, reviewHead)

	h.forceApprove(t, a.action, a.Hash)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = h.tryInvoke(a.action) }()
	}
	wg.Wait()
	e := h.waitPosted(t, a.action)
	rv := assertOneReview(t, gh, a, 1)
	if e.ExternalID == nil || *e.ExternalID != rv.URL {
		t.Fatalf("external_id = %v, want %s", e.ExternalID, rv.URL)
	}
	if r := h.invoke(t, a.action); r.Status != 200 || r.Outcome != "already_posted" {
		t.Fatalf("replay: %d %s %s, want 200 already_posted", r.Status, r.Outcome, r.Error)
	}
	assertOneReview(t, gh, a, 1)
}

func TestReviewOnMovedHeadIsStale(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	a := h.seedReviewAction(t, h.seedUser(t))
	gh.setHead(a.PR, strings.Repeat("9", 40)) // pushed after the review was proposed

	h.forceApprove(t, a.action, a.Hash)
	waitFor(t, "proposed_actions.status = stale", 20*time.Second, func() bool {
		return h.actionStatus(t, a.action) == "stale"
	})
	if r := h.invoke(t, a.action); r.Status != 409 || r.Outcome != "stale" || !strings.Contains(r.Error, "stale_head") {
		t.Fatalf("invoke: %d %s %q, want 409 stale stale_head", r.Status, r.Outcome, r.Error)
	}
	if posts, reviews, statuses := gh.reviewsOn(a.PR, a.Repo); posts != 0 || len(reviews) != 0 || len(statuses) != 0 {
		t.Fatalf("stale review wrote %d review POSTs, %d reviews, %d statuses; want none", posts, len(reviews), len(statuses))
	}
	if e := h.effect(t, a.action); e.ExternalID != nil || e.Status != "failed" {
		t.Fatalf("effect = %+v, want failed with no external_id", e)
	}
}

// The review lands but the status call fails: the retry finds the review by
// marker, posts only the status, and records the original review.
func TestReviewPostedStatusFailedRetriesStatusOnly(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.failStatuses = true })
	a := h.seedReviewAction(t, h.seedUser(t))
	gh.setHead(a.PR, reviewHead)

	h.forceApprove(t, a.action, a.Hash)
	waitFor(t, "effects.status = failed after the status error", 20*time.Second, func() bool {
		return h.effect(t, a.action).Status == "failed"
	})
	if _, reviews, statuses := gh.reviewsOn(a.PR, a.Repo); len(reviews) != 1 || len(statuses) != 0 {
		t.Fatalf("after the failed status: %d reviews, %d statuses, want 1 and 0", len(reviews), len(statuses))
	}

	gh.set(func(f *fakeGitHub) { f.failStatuses = false })
	if r := h.invoke(t, a.action); r.Status != 200 || r.Outcome != "recorded_existing" {
		t.Fatalf("retry: %d %s %s, want 200 recorded_existing", r.Status, r.Outcome, r.Error)
	}
	rv := assertOneReview(t, gh, a, 1)
	if e := h.effect(t, a.action); e.Status != "posted" || e.ExternalID == nil || *e.ExternalID != rv.URL {
		t.Fatalf("effect = %+v, want posted with %s", e, rv.URL)
	}
}

// GitHub refuses the review (403: the App lacks Pull requests: write). The
// effect ends failed with nothing recorded; once the permission is granted
// the operator re-invokes the effector for the same approval
// (runbooks/effect-stuck.md) and exactly one review lands.
func TestReviewForbiddenThenRetryAfterPermissionFixPostsOnce(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.forbidReviews = true })
	a := h.seedReviewAction(t, h.seedUser(t))
	gh.setHead(a.PR, reviewHead)

	h.forceApprove(t, a.action, a.Hash)
	waitFor(t, "effects.status = failed after the 403", 20*time.Second, func() bool {
		return h.effect(t, a.action).Status == "failed"
	})
	if e := h.effect(t, a.action); e.ExternalID != nil || e.Attempts != 1 {
		t.Fatalf("after the 403: effects = %+v, want failed, external_id null, attempts 1", e)
	}
	if posts, reviews, _ := gh.reviewsOn(a.PR, a.Repo); posts != 1 || len(reviews) != 0 {
		t.Fatalf("after the 403: %d review POSTs, %d reviews; want 1 and 0", posts, len(reviews))
	}
	if r := h.invoke(t, a.action); r.Status != 502 || r.Outcome != "failed" || !strings.Contains(r.Error, "403") {
		t.Fatalf("retry while still forbidden: %d %s %q, want 502 failed naming 403", r.Status, r.Outcome, r.Error)
	}

	gh.set(func(f *fakeGitHub) { f.forbidReviews = false }) // permission granted
	if r := h.invoke(t, a.action); r.Status != 200 || r.Outcome != "posted" {
		t.Fatalf("retry after the fix: %d %s %s, want 200 posted", r.Status, r.Outcome, r.Error)
	}
	rv := assertOneReview(t, gh, a, 3)
	if e := h.effect(t, a.action); e.Status != "posted" || e.ExternalID == nil || *e.ExternalID != rv.URL || e.Attempts != 3 {
		t.Fatalf("effect = %+v, want posted with %s, attempts 3", e, rv.URL)
	}
	if r := h.invoke(t, a.action); r.Status != 200 || r.Outcome != "already_posted" {
		t.Fatalf("replay: %d %s %s, want 200 already_posted", r.Status, r.Outcome, r.Error)
	}
	assertOneReview(t, gh, a, 3)
}

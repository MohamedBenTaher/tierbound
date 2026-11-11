package e2e

import (
	"sync"
	"testing"
	"time"
)

// Plan step 2.3: approval -> exactly one GitHub comment, however often and
// however concurrently the effector runs.

func assertOneComment(t *testing.T, gh *fakeGitHub, a action, wantPosts int) fakeComment {
	t.Helper()
	posts, comments := gh.counts(a.Issue)
	if len(comments) != 1 || posts != wantPosts {
		t.Fatalf("fake GitHub has %d comments (%d POSTs) on %s, want 1 comment (%d POSTs)",
			len(comments), posts, a.Issue, wantPosts)
	}
	if comments[0].Body != a.wantComment() {
		t.Fatalf("posted body %q, want the approved payload body plus marker %q", comments[0].Body, a.wantComment())
	}
	return comments[0]
}

func (h harness) waitPosted(t *testing.T, a action) effectRow {
	t.Helper()
	var e effectRow
	waitFor(t, "effects.status = posted", 20*time.Second, func() bool {
		e = h.effect(t, a)
		return e.Status == "posted"
	})
	return e
}

func TestApprovalWebhookPostsExactlyOnce(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash) // fires the pg_net trigger

	e := h.waitPosted(t, a)
	c := assertOneComment(t, gh, a, 1)
	if e.ExternalID == nil || *e.ExternalID != c.URL {
		t.Fatalf("effects.external_id = %v, want the comment URL %s", e.ExternalID, c.URL)
	}
	if s := h.actionStatus(t, a); s != "executed" {
		t.Fatalf("proposed_actions.status = %s, want executed", s)
	}

	// Replays: the effector fired again, twice, leaves the comment count alone.
	for i := 0; i < 2; i++ {
		if r := h.invoke(t, a); r.Status != 200 || r.Outcome != "already_posted" {
			t.Fatalf("replay %d: %d %s %s, want 200 already_posted", i+1, r.Status, r.Outcome, r.Error)
		}
	}
	assertOneComment(t, gh, a, 1)
}

func TestConcurrentInvocationsPostOnce(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.postDelay = 1500 * time.Millisecond })
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash) // the webhook is one more concurrent invocation

	const n = 8
	var wg sync.WaitGroup
	results := make([]invokeResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = h.tryInvoke(a)
		}(i)
	}
	wg.Wait()
	h.waitPosted(t, a)
	assertOneComment(t, gh, a, 1)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("invocation %d: %v", i, err)
		}
		switch results[i].Outcome {
		case "posted", "busy", "already_posted":
		default:
			t.Fatalf("invocation %d: unexpected %d %s %s", i, results[i].Status, results[i].Outcome, results[i].Error)
		}
	}
}

// SPEC section 7: GitHub stores the comment but the response never arrives
// (timeout-after-success). The effect is left 'failed' with no external_id;
// the retry must find the marker and record it instead of posting again.
func TestTimeoutAfterSuccessRetryDoesNotDoublePost(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.hangAfterStore = 4 * time.Second }) // > GITHUB_TIMEOUT_MS
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash)
	waitFor(t, "effects.status = failed after the GitHub timeout", 20*time.Second, func() bool {
		return h.effect(t, a).Status == "failed"
	})
	if e := h.effect(t, a); e.ExternalID != nil {
		t.Fatalf("external_id = %s after a timeout, want null", *e.ExternalID)
	}

	gh.set(func(f *fakeGitHub) { f.hangAfterStore = 0 })
	if r := h.invoke(t, a); r.Status != 200 || r.Outcome != "recorded_existing" {
		t.Fatalf("retry: %d %s %s, want 200 recorded_existing", r.Status, r.Outcome, r.Error)
	}
	c := assertOneComment(t, gh, a, 1)
	if e := h.effect(t, a); e.Status != "posted" || e.ExternalID == nil || *e.ExternalID != c.URL {
		t.Fatalf("after retry effects = %+v, want posted with %s", e, c.URL)
	}
}

// A crash between the post and the status update leaves the row 'claimed'
// with no external_id. While the claim's lease is live nobody else may act
// (that is the concurrency fix); once it expires the takeover finds the
// marker and records the existing comment.
func TestCrashBetweenPostAndRecord(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash)
	h.waitPosted(t, a)
	assertOneComment(t, gh, a, 1)

	// Rewind to the crash state: comment exists, the record step never ran.
	h.exec(t, `update public.effects set status = 'claimed', external_id = null, executed_at = null,
		claimed_at = now() where action_id = $1::uuid`, a.ID)
	h.exec(t, `update public.proposed_actions set status = 'approved' where id = $1::uuid`, a.ID)

	if r := h.invoke(t, a); r.Status != 409 || r.Outcome != "busy" {
		t.Fatalf("live lease: %d %s %s, want 409 busy", r.Status, r.Outcome, r.Error)
	}
	assertOneComment(t, gh, a, 1)

	h.exec(t, `update public.effects set claimed_at = now() - interval '1 hour' where action_id = $1::uuid`, a.ID)
	if r := h.invoke(t, a); r.Status != 200 || r.Outcome != "recorded_existing" {
		t.Fatalf("expired lease: %d %s %s, want 200 recorded_existing", r.Status, r.Outcome, r.Error)
	}
	assertOneComment(t, gh, a, 1)
	if e := h.effect(t, a); e.Status != "posted" || e.ExternalID == nil {
		t.Fatalf("after takeover effects = %+v, want posted with an external_id", e)
	}
	if s := h.actionStatus(t, a); s != "executed" {
		t.Fatalf("proposed_actions.status = %s, want executed", s)
	}
}

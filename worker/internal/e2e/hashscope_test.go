package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// payload_hash commits to {kind, target, payload}: an approval for one issue
// cannot be spent on another. (kind is also pinned by a check constraint to
// 'github_comment', so it cannot be changed in SQL at all in v1; the Go unit
// test TestActionHashCommitsToKindAndTarget covers kind in the hash.)

func (h harness) retarget(t *testing.T, a action, issue int) {
	t.Helper()
	h.exec(t, `update public.proposed_actions set target = jsonb_set(target, '{issue_number}', to_jsonb($2::int))
		where id = $1::uuid`, a.ID, issue)
}

// Approved, but GitHub is down so nothing posted; the target is then moved
// to another issue in SQL. The retry must refuse, and must post again once
// the target is put back (so the refusal is about the target, nothing else).
func TestRetargetAfterApprovalRefusedByEffector(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	gh.set(func(f *fakeGitHub) { f.failPosts = true })
	a := h.seedAction(t, h.seedUser(t))

	h.forceApprove(t, a, a.Hash)
	waitFor(t, "effects.status = failed while GitHub is down", 20*time.Second, func() bool {
		return h.effect(t, a).Status == "failed"
	})
	gh.set(func(f *fakeGitHub) { f.failPosts = false })

	var issue int
	if err := h.db.QueryRow(t.Context(),
		`select (target->>'issue_number')::int from public.proposed_actions where id = $1::uuid`, a.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	h.retarget(t, a, issue+1)
	if r := h.invoke(t, a); r.Status != http.StatusForbidden || r.Outcome != "refused" {
		t.Fatalf("retargeted retry: %d %s %s, want 403 refused", r.Status, r.Outcome, r.Error)
	}
	if _, comments := gh.counts(fmt.Sprintf("%s#%d", fixtureRepo, issue+1)); len(comments) != 0 {
		t.Fatalf("%d comments posted on the new target", len(comments))
	}
	assertNoComment(t, gh, a)

	h.retarget(t, a, issue)
	if r := h.invoke(t, a); r.Status != http.StatusOK || r.Outcome != "posted" {
		t.Fatalf("restored target: %d %s %s, want 200 posted", r.Status, r.Outcome, r.Error)
	}
	if _, comments := gh.counts(a.Issue); len(comments) != 1 {
		t.Fatalf("%d comments on the approved target, want 1", len(comments))
	}
}

// The target moved before the human approved: approve_action with the hash
// they read is refused.
func TestRetargetThenApproveWithOldHashRefused(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)

	h.retarget(t, a, 1)
	r := h.callTool(t, token, "approve_action", map[string]any{"action_id": a.ID, "payload_hash": a.Hash})
	if !r.IsError {
		t.Fatalf("approve_action accepted the old hash after a retarget: %s", r.Text)
	}
	if n := h.approvalCount(t, a); n != 0 {
		t.Fatalf("%d approvals rows after a refused approve, want 0", n)
	}
	assertNoComment(t, gh, a)
}

// reject_action records the hash of what was rejected, not "".
func TestRejectStoresActionHash(t *testing.T) {
	h := setup(t)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)
	if r := h.callTool(t, token, "reject_action", map[string]any{"action_id": a.ID, "reason": "wrong area"}); r.IsError {
		t.Fatalf("reject refused: %s", r.Text)
	}
	var hash, decision string
	if err := h.db.QueryRow(t.Context(),
		`select payload_hash, decision from public.approvals where action_id = $1::uuid`, a.ID).Scan(&hash, &decision); err != nil {
		t.Fatal(err)
	}
	if decision != "rejected" || hash != a.Hash {
		t.Fatalf("approvals row = (%s, %s), want (rejected, %s)", decision, hash, a.Hash)
	}
}

// The TypeScript actionHash (via get_action.recomputed_hash, after a
// Postgres jsonb round trip) matches every golden vector the Go
// hashing.ActionHash is pinned to.
func TestTypeScriptActionHashMatchesGoldenVectors(t *testing.T) {
	h := setup(t)
	owner, token := h.signedInUser(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "action_hash_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []struct {
			Name    string          `json:"name"`
			Kind    string          `json:"kind"`
			Target  json.RawMessage `json:"target"`
			Payload json.RawMessage `json:"payload"`
			SHA256  string          `json:"sha256"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if v.Kind != "github_comment" {
				t.Skipf("kind %q cannot be stored in v1 (check constraint)", v.Kind)
			}
			id := h.insertAction(t, owner, string(v.Target), string(v.Payload), v.SHA256)
			r := h.callTool(t, token, "get_action", map[string]any{"action_id": id})
			if r.IsError {
				t.Fatalf("get_action: %s", r.Text)
			}
			var got struct {
				RecomputedHash string `json:"recomputed_hash"`
			}
			if err := json.Unmarshal([]byte(r.Text), &got); err != nil {
				t.Fatal(err)
			}
			if got.RecomputedHash != v.SHA256 {
				t.Fatalf("TypeScript actionHash drifted\n got: %s\nwant: %s", got.RecomputedHash, v.SHA256)
			}
		})
	}
}

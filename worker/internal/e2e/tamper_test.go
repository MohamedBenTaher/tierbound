package e2e

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
)

// Plan step 2.4: hash tamper and commitment denial (SPEC section 8 demo).

const tamperedBody = "We will ship a fix by Friday."

func (h harness) tamper(t *testing.T, a action) {
	t.Helper()
	h.exec(t, `update public.proposed_actions set payload = jsonb_build_object('body', $2::text)
		where id = $1::uuid`, a.ID, tamperedBody)
}

func (h harness) approvalCount(t *testing.T, a action) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(),
		`select count(*) from public.approvals where action_id = $1::uuid`, a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertNoComment(t *testing.T, gh *fakeGitHub, a action) {
	t.Helper()
	if _, comments := gh.counts(a.Issue); len(comments) != 0 {
		t.Fatalf("%d comments on %s, want none", len(comments), a.Issue)
	}
}

// Case 1: the payload is edited in SQL after the human read its hash.
// approve_action with the old hash is refused, nothing is approved, and
// nothing is posted. The control action, approved the same way untampered,
// posts exactly once (approve via MCP -> webhook -> one comment).
func TestTamperThenApproveWithOldHashRefused(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)

	control := h.seedAction(t, owner)
	if r := h.callTool(t, token, "approve_action",
		map[string]any{"action_id": control.ID, "payload_hash": control.Hash}); r.IsError {
		t.Fatalf("control approve refused: %s", r.Text)
	}
	h.waitPosted(t, control)
	assertOneComment(t, gh, control, 1)

	a := h.seedAction(t, owner)
	h.tamper(t, a)
	r := h.callTool(t, token, "approve_action", map[string]any{"action_id": a.ID, "payload_hash": a.Hash})
	if !r.IsError {
		t.Fatalf("approve_action accepted the old hash after tamper: %s", r.Text)
	}
	if n := h.approvalCount(t, a); n != 0 {
		t.Fatalf("%d approvals rows after a refused approve, want 0", n)
	}
	if res := h.invoke(t, a); res.Status != http.StatusForbidden || res.Outcome != "refused" {
		t.Fatalf("effector on the tampered action: %d %s, want 403 refused", res.Status, res.Outcome)
	}
	assertNoComment(t, gh, a)
}

// Case 2: an approvals row is force-inserted in SQL, bypassing approve_action.
// The effector recomputes the payload hash itself and refuses, both for an
// unrelated hash and for the stale hash a tamper leaves in the payload_hash
// column (comparing against that column instead would pass).
func TestForcedApprovalWithWrongHashRefusedByEffector(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner := h.seedUser(t)

	unrelated, err := hashing.Canonical(map[string]any{"body": "something nobody approved"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		tamper bool
		hash   func(a action) string
	}{
		{"unrelated hash", false, func(action) string { return unrelated }},
		{"stale column hash after tamper", true, func(a action) string { return a.Hash }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := h.seedAction(t, owner)
			if tc.tamper {
				h.tamper(t, a)
			}
			h.forceApprove(t, a, tc.hash(a)) // fires the webhook

			waitFor(t, "the webhook's effector call to be refused (403)", 20*time.Second, func() bool {
				return slices.Contains(h.webhookResponses(t, a), http.StatusForbidden)
			})
			if res := h.invoke(t, a); res.Status != http.StatusForbidden || res.Outcome != "refused" {
				t.Fatalf("direct effector call: %d %s, want 403 refused", res.Status, res.Outcome)
			}
			if e := h.effect(t, a); e.Exists {
				t.Fatalf("effects row %+v exists for a refused action", e)
			}
			assertNoComment(t, gh, a)
		})
	}
}

func wantDenied(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("%s: want SQLSTATE 42501 permission denied, got %v", what, err)
	}
}

// Case 3 (commitment stand-in): the worker's own login -- the credential the
// worker process holds, not `set role` -- cannot write effects or approvals,
// even for a real action.
func TestWorkerLoginCannotCommitEffects(t *testing.T) {
	h := setup(t)
	a := h.seedAction(t, h.seedUser(t))
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.workerDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	_, err = conn.Exec(ctx, `insert into public.effects (action_id, status, external_id)
		values ($1::uuid, 'posted', 'https://github.test/forged')`, a.ID)
	wantDenied(t, "worker insert into effects", err)
	_, err = conn.Exec(ctx, `update public.effects set status = 'posted' where action_id = $1::uuid`, a.ID)
	wantDenied(t, "worker update effects", err)
	_, err = conn.Exec(ctx, `insert into public.approvals (action_id, approver, payload_hash, decision)
		values ($1::uuid, $2::uuid, $3, 'approved')`, a.ID, a.Owner, a.Hash)
	wantDenied(t, "worker insert into approvals", err)
	if e := h.effect(t, a); e.Exists {
		t.Fatalf("effects row %+v exists after denied writes", e)
	}
}

// Approvals are insert-once for their owner (assumption A4 of the
// verify-effector-claim model): after approving, the owner cannot rewrite the
// approved hash or delete the row through the API.
func TestOwnerCannotRewriteApproval(t *testing.T) {
	h := setup(t)
	startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)
	if r := h.callTool(t, token, "approve_action",
		map[string]any{"action_id": a.ID, "payload_hash": a.Hash}); r.IsError {
		t.Fatalf("approve refused: %s", r.Text)
	}
	h.waitPosted(t, a)

	user := map[string]string{"apikey": h.anonKey, "Authorization": "Bearer " + token}
	path := "/rest/v1/approvals?action_id=eq." + a.ID
	if status, raw, err := h.request(http.MethodPatch, path, user,
		[]byte(`{"payload_hash":"0000000000000000000000000000000000000000000000000000000000000000"}`)); err != nil ||
		status >= 500 {
		t.Fatalf("patch: %d %s %v", status, raw, err)
	}
	if status, raw, err := h.request(http.MethodDelete, path, user, nil); err != nil || status >= 500 {
		t.Fatalf("delete: %d %s %v", status, raw, err)
	}

	var hash string
	err := h.db.QueryRow(context.Background(),
		`select payload_hash from public.approvals where action_id = $1::uuid`, a.ID).Scan(&hash)
	if err != nil {
		t.Fatalf("approval row gone after the owner's delete: %v", err)
	}
	if hash != a.Hash {
		t.Fatalf("owner rewrote the approved hash to %s", hash)
	}
}

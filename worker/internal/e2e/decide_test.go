package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

// Atomic decisions (public.tierbound_decide_action) and the closed owner-edit
// hole: owners decide only through the function, never by writing rows.

func (h harness) userHeaders(token string) map[string]string {
	return map[string]string{"apikey": h.anonKey, "Authorization": "Bearer " + token}
}

// rpcDecide calls the decide function through PostgREST as the user. Safe
// from goroutines (no t.Fatal).
func (h harness) rpcDecide(token, actionID, decision, hash string) (int, string, error) {
	body, err := json.Marshal(map[string]any{
		"p_action_id": actionID, "p_decision": decision, "p_expected_hash": hash,
	})
	if err != nil {
		return 0, "", err
	}
	status, raw, err := h.request(http.MethodPost, "/rest/v1/rpc/tierbound_decide_action", h.userHeaders(token), body)
	return status, string(raw), err
}

func TestConcurrentApproveAndRejectYieldOneDecision(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)

	const n = 10
	var wg sync.WaitGroup
	statuses := make([]int, n)
	bodies := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		decision := "approved"
		if i%2 == 1 {
			decision = "rejected"
		}
		wg.Add(1)
		go func(i int, decision string) {
			defer wg.Done()
			statuses[i], bodies[i], errs[i] = h.rpcDecide(token, a.ID, decision, a.Hash)
		}(i, decision)
	}
	wg.Wait()

	wins := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		switch {
		case statuses[i] == http.StatusOK:
			wins++
		case statuses[i] >= 500:
			t.Fatalf("call %d: %d %s, want a clean 4xx refusal for the losers", i, statuses[i], bodies[i])
		}
	}
	if wins != 1 {
		t.Fatalf("%d decisions succeeded, want exactly 1", wins)
	}
	if n := h.approvalCount(t, a); n != 1 {
		t.Fatalf("%d approvals rows, want 1", n)
	}
	var decision, status string
	if err := h.db.QueryRow(context.Background(), `
		select ap.decision, pa.status::text from public.approvals ap
		join public.proposed_actions pa on pa.id = ap.action_id where ap.action_id = $1::uuid`,
		a.ID).Scan(&decision, &status); err != nil {
		t.Fatal(err)
	}
	if decision == "approved" {
		h.waitPosted(t, a)
		assertOneComment(t, gh, a, 1)
		status = h.actionStatus(t, a)
		if status != "executed" {
			t.Fatalf("approved action status %s, want executed", status)
		}
	} else if status != "rejected" {
		t.Fatalf("rejected decision but action status %s", status)
	}
}

func TestDoubleApproveRefusedCleanly(t *testing.T) {
	h := setup(t)
	gh := startFakeGitHub(t, h)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)

	args := map[string]any{"action_id": a.ID, "payload_hash": a.Hash}
	if r := h.callTool(t, token, "approve_action", args); r.IsError {
		t.Fatalf("first approve refused: %s", r.Text)
	}
	r := h.callTool(t, token, "approve_action", args)
	if !r.IsError || !strings.Contains(r.Text, "not proposed") {
		t.Fatalf("second approve: isError=%v %s, want a 'not proposed' refusal", r.IsError, r.Text)
	}
	if r := h.callTool(t, token, "reject_action", map[string]any{"action_id": a.ID, "reason": "late"}); !r.IsError {
		t.Fatalf("reject after approve accepted: %s", r.Text)
	}
	if n := h.approvalCount(t, a); n != 1 {
		t.Fatalf("%d approvals rows, want 1", n)
	}
	h.waitPosted(t, a)
	assertOneComment(t, gh, a, 1)
}

func TestOwnerCannotEditActionsOrWriteApprovals(t *testing.T) {
	h := setup(t)
	owner, token := h.signedInUser(t)
	a := h.seedAction(t, owner)
	path := "/rest/v1/proposed_actions?id=eq." + a.ID

	for name, patch := range map[string]string{
		"payload": `{"payload":{"body":"We will ship a fix by Friday."}}`,
		"target":  `{"target":{"repo":"o/elsewhere","issue_number":1}}`,
		"status":  `{"status":"approved"}`,
	} {
		status, raw, err := h.request(http.MethodPatch, path, h.userHeaders(token), []byte(patch))
		if err != nil {
			t.Fatal(err)
		}
		if status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Fatalf("owner PATCH %s: %d %s, want permission denied", name, status, raw)
		}
	}
	var hash, status string
	if err := h.db.QueryRow(context.Background(), `
		select public.tierbound_action_hash(kind, target, payload), status::text
		from public.proposed_actions where id = $1::uuid`, a.ID).Scan(&hash, &status); err != nil {
		t.Fatal(err)
	}
	if hash != a.Hash || status != "proposed" {
		t.Fatalf("action changed by owner writes: hash %s status %s", hash, status)
	}

	approval, err := json.Marshal(map[string]any{
		"action_id": a.ID, "approver": owner, "payload_hash": a.Hash, "decision": "approved",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, raw, err := h.request(http.MethodPost, "/rest/v1/approvals", h.userHeaders(token), approval)
	if err != nil {
		t.Fatal(err)
	}
	if st != http.StatusForbidden && st != http.StatusUnauthorized {
		t.Fatalf("owner direct INSERT into approvals: %d %s, want permission denied", st, raw)
	}
	if n := h.approvalCount(t, a); n != 0 {
		t.Fatalf("%d approvals rows after a denied insert", n)
	}

	// Someone else's action looks like it does not exist.
	_, otherToken := h.signedInUser(t)
	st, raw2, err := h.rpcDecide(otherToken, a.ID, "approved", a.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if st == http.StatusOK || !strings.Contains(raw2, "action not found") {
		t.Fatalf("non-owner decide: %d %s, want 'action not found'", st, raw2)
	}
}

// The SQL twin of ActionHash, which decide recomputes under the row lock,
// matches the shared golden vectors.
func TestSQLActionHashMatchesGoldenVectors(t *testing.T) {
	h := setup(t)
	raw, err := os.ReadFile("../../../testdata/action_hash_vectors.json")
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
		var got string
		if err := h.db.QueryRow(context.Background(),
			`select public.tierbound_action_hash($1, $2::jsonb, $3::jsonb)`,
			v.Kind, string(v.Target), string(v.Payload)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != v.SHA256 {
			t.Errorf("%s: SQL action hash drifted\n got: %s\nwant: %s", v.Name, got, v.SHA256)
		}
	}
}

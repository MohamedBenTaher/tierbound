package propose

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestBlockingReviewRefusedWithoutCapability is plan step 6.1: the database
// refuses a blocking review (REQUEST_CHANGES or a failure status) unless the
// agent holds 'block' for its current config_hash. A COMMENT review needs no
// capability. Blocking is therefore inactive until a grant exists.
func TestBlockingReviewRefusedWithoutCapability(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	ctx := context.Background()
	defer func() {
		_, _ = deps.Pool.Exec(ctx, `delete from public.agent_capabilities where agent_id = $1::uuid`, agentID)
	}()
	runID, err := startRun(ctx, deps.Pool, agentID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(key, payload string) error {
		tx, err := deps.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `set local role tierbound_worker`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `
			insert into public.proposed_actions (run_id, kind, target, payload, payload_hash, idempotency_key)
			values ($1::uuid, 'github_pr_review', '{"repo":"o/r","pr_number":1,"head_sha":"x"}', $2::jsonb, 'h', $3)`,
			runID, payload, key)
		return err
	}
	comment := `{"event":"COMMENT","body":"b","comments":[],"status":{"state":"success"},"findings":[]}`
	blocking := `{"event":"REQUEST_CHANGES","body":"b","comments":[],"status":{"state":"failure"},"findings":[]}`
	failureOnly := `{"event":"COMMENT","body":"b","comments":[],"status":{"state":"failure"},"findings":[]}`

	if err := insert("rv-comment", comment); err != nil {
		t.Fatalf("COMMENT review without capability: %v", err)
	}
	for name, p := range map[string]string{"REQUEST_CHANGES": blocking, "failure status": failureOnly} {
		var pgErr *pgconn.PgError
		if err := insert("rv-"+name, p); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s without capability: want 42501, got %v", name, err)
		}
	}

	// A capability earned by an older config does not carry over.
	if _, err := deps.Pool.Exec(ctx, `insert into public.agent_capabilities (agent_id, capability, granted_hash)
		values ($1::uuid, 'block', 'some-older-hash')`, agentID); err != nil {
		t.Fatal(err)
	}
	if err := insert("rv-old", blocking); err == nil {
		t.Fatal("block granted for another config_hash admitted a blocking review")
	}
	if _, err := deps.Pool.Exec(ctx, `update public.agent_capabilities set granted_hash = a.config_hash
		from public.agents a where a.id = agent_capabilities.agent_id and a.id = $1::uuid`, agentID); err != nil {
		t.Fatal(err)
	}
	if err := insert("rv-granted", blocking); err != nil {
		t.Fatalf("blocking review with block for the current config_hash: %v", err)
	}
}

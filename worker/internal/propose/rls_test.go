package propose

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestWorkerRoleDeniedOnApprovalsAndEffects is the plan step 1.6 denial test:
// the worker role must never write approvals or effects (AGENTS.md invariant
// 1). The build fails if any of these statements succeed.
func TestWorkerRoleDeniedOnApprovalsAndEffects(t *testing.T) {
	evalsOK := "test-config-hash"
	deps, agentID, cleanup := setup(t, &evalsOK)
	defer cleanup()
	ctx := context.Background()

	asWorker := func(fn func(tx pgx.Tx) error) error {
		t.Helper()
		tx, err := deps.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `set local role tierbound_worker`); err != nil {
			t.Fatal(err)
		}
		return fn(tx)
	}

	denied := func(name string, fn func(tx pgx.Tx) error) {
		t.Helper()
		err := asWorker(fn)
		if err == nil {
			t.Fatalf("%s succeeded as tierbound_worker; the worker must not reach approvals/effects", name)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: want SQLSTATE 42501 (permission denied), got %v", name, err)
		}
	}

	denied("insert into approvals", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			insert into public.approvals (action_id, approver, payload_hash, decision)
			values ('00000000-0000-0000-0000-000000000009',
			        '00000000-0000-0000-0000-000000000009', 'x', 'approved')`)
		return err
	})
	denied("insert into effects", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`insert into public.effects (action_id) values ('00000000-0000-0000-0000-000000000009')`)
		return err
	})
	denied("select from approvals", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `select * from public.approvals`)
		return err
	})
	denied("select from effects", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `select * from public.effects`)
		return err
	})

	// Positive control: the same role still writes its own ledger tables, so
	// the denials above are grants and not a broken connection.
	err := asWorker(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`insert into public.runs (agent_id, attempt, status) values ($1::uuid, 1, 'running')`,
			agentID)
		return err
	})
	if err != nil {
		t.Fatalf("worker could not insert into runs: %v", err)
	}
}

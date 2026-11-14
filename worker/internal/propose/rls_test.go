package propose

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWorkerRoleDeniedOnApprovalsAndEffects is the plan step 1.6 denial test:
// the worker role must never write approvals or effects (AGENTS.md invariant
// 1). The build fails if any of these statements succeed.
func TestWorkerRoleDeniedOnApprovalsAndEffects(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
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

// TestWorkerRoleCanUseItsQueues drives every queue operation the worker
// performs as tierbound_worker itself (the other queue tests run as the
// database owner): read and archive on jobs and jobs_issue, and the
// dead-letter send to jobs_dead. Regression test for the missing grants
// on queues created after the roles migration.
func TestWorkerRoleCanUseItsQueues(t *testing.T) {
	deps, _, cleanup := setup(t, true)
	defer cleanup()

	for _, queue := range []string{"jobs", "jobs_issue"} {
		workerQueueRoundTrip(t, deps.Pool, queue)
	}
}

// workerQueueRoundTrip sends one message as the owner, then reads, extends,
// archives it and dead-letters a copy as tierbound_worker (rolled back). The
// message is deleted on the way out even when a step fails.
func workerQueueRoundTrip(t *testing.T, pool *pgxpool.Pool, queue string) {
	t.Helper()
	ctx := context.Background()
	var msgID int64
	if err := pool.QueryRow(ctx, `select pgmq.send($1, '{"delivery_id":"test-grants"}'::jsonb)`,
		queue).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `delete from pgmq.q_`+queue+` where msg_id = $1`, msgID)
	}()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := strconv.FormatInt(msgID, 10)
	steps := []struct{ name, sql string }{
		{"set role", `set local role tierbound_worker`},
		{"read " + queue, `select msg_id from pgmq.read('` + queue + `', 30, 10)`},
		{"set_vt " + queue, `select msg_id from pgmq.set_vt('` + queue + `', ` + id + `, 60)`},
		{"archive " + queue, `select pgmq.archive('` + queue + `', ` + id + `::bigint)`},
		{"send jobs_dead", `select pgmq.send('jobs_dead', '{"delivery_id":"test-grants"}'::jsonb)`},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, s.sql); err != nil {
			t.Fatalf("%s as tierbound_worker: %v", s.name, err)
		}
	}
}

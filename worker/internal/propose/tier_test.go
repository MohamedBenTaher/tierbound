package propose

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestTierTriggerRefusesKindAboveTier is plan step 4.1: the kind policy is
// enforced in the database, so even a worker that skipped its own activation
// rule cannot record a kind its agent tier does not allow.
func TestTierTriggerRefusesKindAboveTier(t *testing.T) {
	deps, agentID, cleanup := setup(t, true)
	defer cleanup()
	ctx := context.Background()

	runID, err := startRun(ctx, deps.Pool, agentID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(kind, key string) error {
		tx, err := deps.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `set local role tierbound_worker`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `
			insert into public.proposed_actions
				(run_id, kind, target, payload, payload_hash, idempotency_key)
			values ($1::uuid, $2, '{"repo":"o/r"}', '{"title":"t","body":"b","labels":[]}', 'h', $3)`,
			runID, kind, key)
		return err
	}
	wantCode := func(name string, err error, code string) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Fatalf("%s: want SQLSTATE %s, got %v", name, code, err)
		}
	}

	// Positive control: a draft agent may propose a draft kind.
	if err := insert(KindGitHubIssue, "tier-ok"); err != nil {
		t.Fatalf("draft agent proposing github_issue: %v", err)
	}
	wantCode("unknown kind", insert("github_merge", "tier-unknown"), "23503")

	if _, err := deps.Pool.Exec(ctx, `update public.agents set tier = 'read' where id = $1::uuid`, agentID); err != nil {
		t.Fatal(err)
	}
	wantCode("read agent proposing github_issue", insert(KindGitHubIssue, "tier-read"), "42501")
	wantCode("read agent proposing github_comment", insert(KindGitHubComment, "tier-read-2"), "42501")
}

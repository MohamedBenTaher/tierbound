package propose

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failingDrafter struct{}

func (failingDrafter) DraftComment(_ context.Context, _, _ string) (string, error) {
	return "", errors.New("simulated crash mid-draft")
}

// TestRetryAfterCrashKeepsOneAction is the automated shape of the kill -9
// proof (plan step 1.5): a job that dies mid-run is redelivered after its
// visibility timeout, and the retry produces exactly one action with the
// later attempt recorded in the run ledger.
func TestRetryAfterCrashKeepsOneAction(t *testing.T) {
	evalsOK := "test-config-hash"
	deps, agentID, cleanup := setup(t, &evalsOK)
	defer cleanup()

	enqueue(t, deps, Job{Repo: "MohamedBenTaher/tierbound", IssueNumber: 9, DeliveryID: "test-retry"})

	// The dying attempt reads the message (read_ct 1) and never archives it.
	msgs, err := deps.Queue.Read(context.Background(), "jobs", 60, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("read %d messages, want 1", len(msgs))
	}

	// Shorten the lease instead of waiting out the production visibility
	// timeout (the runbook waits the real 120s).
	if err := deps.Queue.SetVT(context.Background(), "jobs", msgs[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	// First retry attempt still dies mid-draft...
	deps.Drafter = failingDrafter{}
	processed, err := ProcessOne(context.Background(), deps)
	if !processed || err == nil {
		t.Fatalf("crashing attempt: processed=%v err=%v, want processed and an error", processed, err)
	}
	if err := deps.Queue.SetVT(context.Background(), "jobs", msgs[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	// ...then the surviving retry completes.
	deps.Drafter = fakeDrafter{body: fakeDraftBody}
	if _, err := ProcessOne(context.Background(), deps); err != nil {
		t.Fatal(err)
	}

	if n := countActions(t, deps, agentID); n != 1 {
		t.Fatalf("retries produced %d actions, want 1", n)
	}
	var attempt int
	err = deps.Pool.QueryRow(context.Background(), `
		select attempt from public.runs where agent_id = $1::uuid
		order by started_at desc, id desc limit 1`, agentID).Scan(&attempt)
	if err != nil {
		t.Fatal(err)
	}
	if attempt < 2 {
		t.Fatalf("final run attempt = %d, want >= 2 (proof of redelivery)", attempt)
	}
}

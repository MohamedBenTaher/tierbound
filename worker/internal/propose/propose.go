// Package propose turns one queue job into at most one proposed_action.
//
// This is the exactly-once seam (AGENTS.md invariant 3): pgmq delivers at
// least once (visibility timeout), the unique idempotency_key on
// proposed_actions collapses retries, and a 23505 unique violation on insert
// is success, not failure. The message is archived only after the action row
// exists, so a crash in between retries safely.
package propose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
	"github.com/MohamedBenTaher/tierbound/worker/internal/hashing"
	"github.com/MohamedBenTaher/tierbound/worker/internal/pgmq"
)

const (
	// KindGitHubComment is the only action kind in v1 (SPEC section 2).
	KindGitHubComment = "github_comment"

	// MaxAttempts is the read count after which a failing job is dead-lettered
	// instead of retried.
	MaxAttempts = 3

	queueJobs = "jobs"
	queueDead = "jobs_dead"

	readVTSeconds = 60
	slowVTSeconds = 120
)

// Job is the queue message ingest enqueues (plan step 1.3).
type Job struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	DeliveryID  string `json:"delivery_id"`
}

// Fetcher fetches one issue (githubro.Client in production).
type Fetcher interface {
	FetchIssue(ctx context.Context, repo string, number int) (githubro.Issue, error)
}

// Drafter drafts one comment body (llm.Client in production).
type Drafter interface {
	DraftComment(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

// Deps wires the collaborators ProcessOne needs.
type Deps struct {
	Pool    *pgxpool.Pool
	Queue   *pgmq.Client
	Fetcher Fetcher
	Drafter Drafter
	Config  agentconfig.Config
}

// ProcessOne reads at most one job and turns it into at most one
// proposed_action. It returns false when the queue was empty. A non-nil error
// means that job did not complete; pgmq retries it after the visibility
// timeout until MaxAttempts, then it is dead-lettered.
func ProcessOne(ctx context.Context, d Deps) (bool, error) {
	msgs, err := d.Queue.Read(ctx, queueJobs, readVTSeconds, 1)
	if err != nil {
		return false, err
	}
	if len(msgs) == 0 {
		return false, nil
	}
	return true, processMessage(ctx, d, msgs[0])
}

func processMessage(ctx context.Context, d Deps, msg pgmq.Message) error {
	var job Job
	if err := json.Unmarshal(msg.Body, &job); err != nil {
		return deadLetter(ctx, d, msg, fmt.Errorf("malformed job: %w", err))
	}
	if job.Repo == "" || job.IssueNumber < 1 {
		return deadLetter(ctx, d, msg, errors.New("job missing repo/issue_number"))
	}

	agent, err := lookupAgent(ctx, d.Pool, d.Config.Name)
	if err != nil {
		return err // transient: the message reappears after the visibility timeout
	}
	if agent == nil {
		return deadLetter(ctx, d, msg, fmt.Errorf("no agent named %q", d.Config.Name))
	}

	runID, err := startRun(ctx, d.Pool, agent.ID, msg.ID, msg.ReadCount)
	if err != nil {
		return err
	}
	if err := attempt(ctx, d, msg, job, agent, runID); err != nil {
		// Best-effort ledger update; the queue retry is the real recovery.
		_ = finishRun(ctx, d.Pool, runID, "failed", err)
		if msg.ReadCount >= MaxAttempts {
			return deadLetter(ctx, d, msg, err)
		}
		return err
	}
	return nil
}

func attempt(ctx context.Context, d Deps, msg pgmq.Message, job Job, agent *agentRow, runID string) error {
	configHash, err := d.Config.Hash()
	if err != nil {
		return err
	}
	// SPEC sections 2 and 5 (plan step 3.3): anything the activation rule
	// refuses is skipped, not retried.
	if reason := activationError(agent, configHash); reason != nil {
		if err := finishRun(ctx, d.Pool, runID, "skipped", reason); err != nil {
			return err
		}
		return d.Queue.Archive(ctx, queueJobs, msg.ID)
	}

	issue, err := d.Fetcher.FetchIssue(ctx, job.Repo, job.IssueNumber)
	if err != nil {
		return err
	}
	// The draft call is slow; extend the lease so the message is not redelivered
	// mid-draft.
	if err := d.Queue.SetVT(ctx, queueJobs, msg.ID, slowVTSeconds); err != nil {
		return err
	}
	body, err := d.Drafter.DraftComment(ctx, d.Config.Prompt, FormIssueMessage(issue))
	if err != nil {
		return err
	}

	payload := map[string]any{"body": body}
	target := map[string]any{"repo": job.Repo, "issue_number": job.IssueNumber}
	payloadHash, err := hashing.ActionHash(KindGitHubComment, target, payload)
	if err != nil {
		return err
	}
	// Plan step 1.4: idempotency_key = agent_id:repo:issue:config_hash.
	key := fmt.Sprintf("%s:%s:%d:%s", agent.ID, job.Repo, job.IssueNumber, configHash)

	err = insertAction(ctx, d.Pool, runID, target, payload, payloadHash, key)
	if err != nil && !isUniqueViolation(err) {
		return err
	}
	// A unique violation means an earlier attempt already inserted this exact
	// action (at-least-once delivery): exactly one action is the invariant.
	if err := finishRun(ctx, d.Pool, runID, "succeeded", nil); err != nil {
		return err
	}
	return d.Queue.Archive(ctx, queueJobs, msg.ID)
}

func deadLetter(ctx context.Context, d Deps, msg pgmq.Message, cause error) error {
	if err := d.Queue.Send(ctx, queueDead, msg.Body); err != nil {
		return fmt.Errorf("dead-letter failed: %w (cause: %w)", err, cause)
	}
	if err := d.Queue.Archive(ctx, queueJobs, msg.ID); err != nil {
		return fmt.Errorf("archive after dead-letter failed: %w (cause: %w)", err, cause)
	}
	return cause
}

// FormIssueMessage renders the issue as the drafter's user message. The eval
// runner uses it too, so evals see exactly what the worker sends.
func FormIssueMessage(issue githubro.Issue) string {
	return fmt.Sprintf("Repository: %s\nIssue: #%d\nTitle: %s\nLabels: %s\n\n%s",
		issue.Repo, issue.Number, issue.Title, strings.Join(issue.Labels, ", "), issue.Body)
}

// activationError is the activation rule (SPEC section 5, plan step 3.3): a
// draft agent may propose only when the config this worker runs is the one
// registered for the agent (loadedHash = config_hash) and that exact hash
// passed the eval gate (evals_passed_hash = config_hash). nil means allowed.
func activationError(agent *agentRow, loadedHash string) error {
	switch {
	case agent.Tier != agentconfig.TierDraft:
		return fmt.Errorf("activation: agent tier %q cannot propose (SPEC section 5)", agent.Tier)
	case loadedHash != agent.ConfigHash:
		return fmt.Errorf("activation: running config_hash %s is not the registered %s (SPEC section 5)", loadedHash, agent.ConfigHash)
	case agent.EvalsPassedHash == nil || *agent.EvalsPassedHash != agent.ConfigHash:
		return fmt.Errorf("activation: config_hash %s has not passed the eval gate (SPEC section 5)", agent.ConfigHash)
	}
	return nil
}

type agentRow struct {
	ID              string
	Tier            agentconfig.Tier
	ConfigHash      string
	EvalsPassedHash *string
}

func lookupAgent(ctx context.Context, pool *pgxpool.Pool, name string) (*agentRow, error) {
	rows, err := pool.Query(ctx, `
		select id::text, tier::text, config_hash, evals_passed_hash
		from public.agents where name = $1 limit 2`, name)
	if err != nil {
		return nil, fmt.Errorf("lookup agent %q: %w", name, err)
	}
	defer rows.Close()
	var found *agentRow
	for rows.Next() {
		var a agentRow
		if err := rows.Scan(&a.ID, &a.Tier, &a.ConfigHash, &a.EvalsPassedHash); err != nil {
			return nil, fmt.Errorf("lookup agent %q: %w", name, err)
		}
		if found != nil {
			return nil, fmt.Errorf("lookup agent %q: multiple agents share this name", name)
		}
		found = &a
	}
	return found, rows.Err()
}

func startRun(ctx context.Context, pool *pgxpool.Pool, agentID string, msgID int64, attempt int32) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		insert into public.runs (agent_id, job_msg_id, attempt, status)
		values ($1::uuid, $2, $3, 'running') returning id::text`,
		agentID, msgID, attempt).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}
	return id, nil
}

func finishRun(ctx context.Context, pool *pgxpool.Pool, runID, status string, cause error) error {
	var errText *string
	if cause != nil {
		s := cause.Error()
		errText = &s
	}
	_, err := pool.Exec(ctx, `
		update public.runs set status = $2, error = $3, finished_at = now()
		where id = $1::uuid`, runID, status, errText)
	if err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}
	return nil
}

func insertAction(ctx context.Context, pool *pgxpool.Pool, runID string,
	target, payload map[string]any, payloadHash, idempotencyKey string,
) error {
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("marshal target: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	_, err = pool.Exec(ctx, `
		insert into public.proposed_actions
			(run_id, kind, target, payload, payload_hash, idempotency_key)
		values ($1::uuid, $2, $3::jsonb, $4::jsonb, $5, $6)`,
		runID, KindGitHubComment, string(targetJSON), string(payloadJSON),
		payloadHash, idempotencyKey)
	if err != nil {
		return fmt.Errorf("insert proposed_action: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

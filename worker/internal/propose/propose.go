// Package propose turns one queue job into at most one proposed_action, of
// the one kind the agent's config proposes (proposal.go).
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
	// KindGitHubComment is the v1 action kind (SPEC section 2).
	KindGitHubComment = "github_comment"
	// KindGitHubIssue opens one new issue (plan step 4.1, V2_ACTIONS 4.1).
	KindGitHubIssue = "github_issue"
	// KindGitHubPRReview reviews one pull request (plan Phase 5, V2_ACTIONS 4.2).
	KindGitHubPRReview = "github_pr_review"

	// MaxAttempts is the read count after which a failing job is dead-lettered
	// instead of retried.
	MaxAttempts = 3

	queueDead = "jobs_dead"

	readVTSeconds = 60
	slowVTSeconds = 300
)

// Job is the queue message ingest enqueues (plan step 1.3). Review jobs
// carry the pull request and the two shas instead of an issue number.
type Job struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	DeliveryID  string `json:"delivery_id"`
	PRNumber    int    `json:"pr_number"`
	HeadSha     string `json:"head_sha"`
	BaseSha     string `json:"base_sha"`
}

// subject is "repo#n" for logs.
func (j Job) subject() string {
	return fmt.Sprintf("%s#%d", j.Repo, max(j.IssueNumber, j.PRNumber))
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
	// PRs reads pull request diffs and rule files (review agents only).
	PRs     PRFetcher
	Drafter Drafter
	Config  agentconfig.Config
	// Log receives one line per job outcome (proposed, duplicate, abstained,
	// skipped, dead-lettered). Failures are returned as errors instead. nil
	// discards.
	Log func(line string)
}

func (d Deps) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, args...))
	}
}

// ProcessOne reads at most one job and turns it into at most one
// proposed_action. It returns false when the queue was empty. A non-nil error
// means that job did not complete; pgmq retries it after the visibility
// timeout until MaxAttempts, then it is dead-lettered.
func ProcessOne(ctx context.Context, d Deps) (bool, error) {
	msgs, err := d.Queue.Read(ctx, d.Config.QueueName(), readVTSeconds, 1)
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
	if err := validateJob(kindFor(d.Config), job); err != nil {
		return deadLetter(ctx, d, msg, err)
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
		d.logf("skipped %s: %v", job.subject(), reason)
		return skip(ctx, d, msg, runID, reason)
	}

	p, err := draftProposal(ctx, d, msg, job, agent.ID)
	if errors.Is(err, errAbstained) {
		d.logf("abstained on %s: %s", job.subject(), strings.TrimPrefix(err.Error(), errAbstained.Error()+": "))
		return skip(ctx, d, msg, runID, err)
	}
	if err != nil {
		return err
	}
	payloadHash, err := hashing.ActionHash(p.kind, p.target, p.payload)
	if err != nil {
		return err
	}
	key := idempotencyKey(agent.ID, job, configHash)
	if err := recordStats(ctx, d.Pool, runID, p.stats); err != nil {
		return err
	}

	actionID, err := insertAction(ctx, d.Pool, runID, p, payloadHash, key)
	switch {
	case isUniqueViolation(err):
		d.logf("already proposed %s for %s (duplicate delivery)", p.kind, job.subject())
	case err != nil:
		return err
	default:
		d.logf("proposed %s %s for %s%s", p.kind, actionID, job.subject(), p.note)
	}
	// A unique violation means an earlier attempt already inserted this exact
	// action (at-least-once delivery): exactly one action is the invariant.
	if err := finishRun(ctx, d.Pool, runID, "succeeded", nil); err != nil {
		return err
	}
	return d.Queue.Archive(ctx, d.Config.QueueName(), msg.ID)
}

// draftProposal fetches the job's input, drafts with the model, and builds the
// action envelope for the agent's kind.
func draftProposal(ctx context.Context, d Deps, msg pgmq.Message, job Job, agentID string) (proposal, error) {
	if kindFor(d.Config) == KindGitHubPRReview {
		return proposeReview(ctx, d, msg, job, agentID)
	}
	issue, err := d.Fetcher.FetchIssue(ctx, job.Repo, job.IssueNumber)
	if err != nil {
		return proposal{}, err
	}
	draft, err := draftSlow(ctx, d, msg, FormIssueMessage(issue))
	if err != nil {
		return proposal{}, err
	}
	return buildProposal(kindFor(d.Config), job, draft)
}

// draftSlow extends the lease, since the model call is slow and the message
// must not be redelivered mid-draft, then drafts.
func draftSlow(ctx context.Context, d Deps, msg pgmq.Message, userMessage string) (string, error) {
	if err := d.Queue.SetVT(ctx, d.Config.QueueName(), msg.ID, slowVTSeconds); err != nil {
		return "", err
	}
	return d.Drafter.DraftComment(ctx, d.Config.Prompt, userMessage)
}

// idempotencyKey: agent_id:repo:issue:config_hash (plan step 1.4); a review
// is per head sha, so a new push gets a new review
// (agent_id:repo:pr:head_sha:config_hash).
func idempotencyKey(agentID string, job Job, configHash string) string {
	if job.PRNumber > 0 {
		return fmt.Sprintf("%s:%s:%d:%s:%s", agentID, job.Repo, job.PRNumber, job.HeadSha, configHash)
	}
	return fmt.Sprintf("%s:%s:%d:%s", agentID, job.Repo, job.IssueNumber, configHash)
}

// skip records the run as skipped with reason and archives the job: nothing
// to propose, and retrying would not change that.
func skip(ctx context.Context, d Deps, msg pgmq.Message, runID string, reason error) error {
	if err := finishRun(ctx, d.Pool, runID, "skipped", reason); err != nil {
		return err
	}
	return d.Queue.Archive(ctx, d.Config.QueueName(), msg.ID)
}

func deadLetter(ctx context.Context, d Deps, msg pgmq.Message, cause error) error {
	d.logf("dead-lettered message %d: %v", msg.ID, cause)
	if err := d.Queue.Send(ctx, queueDead, msg.Body); err != nil {
		return fmt.Errorf("dead-letter failed: %w (cause: %w)", err, cause)
	}
	if err := d.Queue.Archive(ctx, d.Config.QueueName(), msg.ID); err != nil {
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

// recordStats stores the run's counters (runs.stats), such as how many
// review findings grounding dropped.
func recordStats(ctx context.Context, pool *pgxpool.Pool, runID string, stats map[string]any) error {
	if stats == nil {
		return nil
	}
	b, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("marshal stats: %w", err)
	}
	if _, err := pool.Exec(ctx, `update public.runs set stats = $2::jsonb where id = $1::uuid`, runID, string(b)); err != nil {
		return fmt.Errorf("record stats for run %s: %w", runID, err)
	}
	return nil
}

// insertAction inserts one proposed_action and returns its id.
func insertAction(ctx context.Context, pool *pgxpool.Pool, runID string,
	p proposal, payloadHash, idempotencyKey string,
) (string, error) {
	targetJSON, err := json.Marshal(p.target)
	if err != nil {
		return "", fmt.Errorf("marshal target: %w", err)
	}
	payloadJSON, err := json.Marshal(p.payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	var id string
	err = pool.QueryRow(ctx, `
		insert into public.proposed_actions
			(run_id, kind, target, payload, payload_hash, idempotency_key)
		values ($1::uuid, $2, $3::jsonb, $4::jsonb, $5, $6)
		returning id::text`,
		runID, p.kind, string(targetJSON), string(payloadJSON),
		payloadHash, idempotencyKey).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert proposed_action: %w", err)
	}
	return id, nil
}

func isUniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

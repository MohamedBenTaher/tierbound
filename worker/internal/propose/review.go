package propose

import (
	"context"
	"errors"
	"fmt"

	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
	"github.com/MohamedBenTaher/tierbound/worker/internal/pgmq"
	"github.com/MohamedBenTaher/tierbound/worker/internal/review"
)

// PRFetcher reads a pull request's diff and repo files at a commit
// (githubro.Client in production).
type PRFetcher interface {
	FetchPRFiles(ctx context.Context, repo string, number int) ([]githubro.PRFile, error)
	FetchFile(ctx context.Context, repo, path, ref string) (string, error)
}

// proposeReview builds one github_pr_review (V2_ACTIONS 4.2): read the diff
// and the rules at base_sha, ask the model for findings, drop every finding
// that is not grounded, and let policy pick the verdict.
func proposeReview(ctx context.Context, d Deps, msg pgmq.Message, job Job, agentID string) (proposal, error) {
	if d.PRs == nil {
		return proposal{}, errors.New("review agent has no PR fetcher")
	}
	in, err := reviewInput(ctx, d.PRs, job, d.Config.Review.RuleSources)
	if err != nil {
		return proposal{}, err
	}
	raw, err := draftSlow(ctx, d, msg, in.Message())
	if err != nil {
		return proposal{}, err
	}
	out, err := review.ParseOutput(raw)
	if err != nil {
		return proposal{}, err
	}
	canBlock, err := canBlock(ctx, d, agentID)
	if err != nil {
		return proposal{}, err
	}
	rc := in.Context()
	kept, dropped := review.Ground(out, rc)
	policy := review.Policy{BlockingRules: d.Config.Review.BlockingRules, Status: d.Config.Review.Status}.Resolve(rc)
	verdict := review.Decide(kept, policy, canBlock)
	return proposal{
		kind:    KindGitHubPRReview,
		target:  map[string]any{"repo": job.Repo, "pr_number": job.PRNumber, "head_sha": job.HeadSha},
		payload: review.Payload(kept, out.Summary, verdict),
		stats:   reviewStats(len(out.Findings), dropped),
		note:    fmt.Sprintf(" (%d findings kept, %d ungrounded dropped, %s)", len(kept), len(dropped), verdict.Event),
	}, nil
}

// reviewInput reads the diff and every configured rule file at base_sha:
// rules are read at the base so a PR cannot weaken the rules it is judged
// by. A rule file missing at the base is skipped.
func reviewInput(ctx context.Context, prs PRFetcher, job Job, sources []string) (review.Input, error) {
	files, err := prs.FetchPRFiles(ctx, job.Repo, job.PRNumber)
	if err != nil {
		return review.Input{}, err
	}
	in := review.Input{Repo: job.Repo, PR: job.PRNumber, HeadSha: job.HeadSha, BaseSha: job.BaseSha}
	for _, f := range files {
		in.Files = append(in.Files, review.File{Path: f.Path, Patch: f.Patch})
	}
	for _, src := range sources {
		text, err := prs.FetchFile(ctx, job.Repo, src, job.BaseSha)
		if errors.Is(err, githubro.ErrNotFound) {
			continue
		}
		if err != nil {
			return review.Input{}, err
		}
		in.Rules = append(in.Rules, review.RuleFile{Source: src, Text: text})
	}
	return in, nil
}

// canBlock asks the database whether this agent holds the block capability
// for its current config_hash. Nobody does until a config clears the
// stricter eval bar; the insert trigger enforces the same rule regardless.
func canBlock(ctx context.Context, d Deps, agentID string) (bool, error) {
	var ok bool
	if err := d.Pool.QueryRow(ctx, `select public.tierbound_can_block($1::uuid)`, agentID).Scan(&ok); err != nil {
		return false, fmt.Errorf("can_block: %w", err)
	}
	return ok, nil
}

func reviewStats(proposed int, dropped []review.Dropped) map[string]any {
	reasons := make([]any, 0, len(dropped))
	for _, dr := range dropped {
		reasons = append(reasons, fmt.Sprintf("%s:%d: %s", dr.Finding.Path, dr.Finding.Line, dr.Reason))
	}
	return map[string]any{
		"findings_proposed": proposed,
		"ungrounded":        len(dropped),
		"ungrounded_why":    reasons,
	}
}

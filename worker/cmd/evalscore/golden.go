package main

import (
	"fmt"
	"strings"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/evals"
	"github.com/MohamedBenTaher/tierbound/worker/internal/propose"
	"github.com/MohamedBenTaher/tierbound/worker/internal/review"
)

// golden is one loaded golden set, whichever scorer it uses.
type golden interface {
	summary() string
	prompts() []prompt
	grade(outputs map[string]string, cfg agentconfig.Config) gates
}

// prompt is one fixture's user message, exactly as the worker builds it.
type prompt struct{ id, message string }

// gates: propose admits the config; block is the stricter bar the block
// capability needs (review sets only).
type gates struct{ propose, block bool }

func loadGolden(dir, rubricPath string) (golden, error) {
	scorer, err := evals.RubricScorer(rubricPath)
	if err != nil {
		return nil, err
	}
	if scorer == evals.ScorerReview {
		s, err := evals.LoadReview(dir, rubricPath)
		return reviewGolden{s}, err
	}
	s, err := evals.Load(dir, rubricPath)
	return perFixtureGolden{s}, err
}

type perFixtureGolden struct{ *evals.Set }

func (g perFixtureGolden) summary() string {
	return fmt.Sprintf("loaded %d fixtures, threshold %.2f, %d global must-not rules",
		len(g.Fixtures), g.Rubric.Threshold, len(g.Rubric.MustNotSay))
}

func (g perFixtureGolden) prompts() []prompt {
	out := make([]prompt, 0, len(g.Fixtures))
	for _, f := range g.Fixtures {
		out = append(out, prompt{f.ID, propose.FormIssueMessage(f.Issue.GitHub())})
	}
	return out
}

func (g perFixtureGolden) grade(outputs map[string]string, _ agentconfig.Config) gates {
	rep := g.Score(outputs)
	printReport(rep)
	return gates{propose: rep.Pass}
}

type reviewGolden struct{ *evals.ReviewSet }

func (g reviewGolden) summary() string {
	r := g.Rubric
	return fmt.Sprintf("loaded %d review fixtures; propose gate p>=%.2f r>=%.2f grounding>=%.2f; block gate blocker p>=%.2f r>=%.2f false blocks<=%d",
		len(g.Fixtures), r.Propose.Precision, r.Propose.Recall, r.Propose.Grounding,
		r.Block.BlockerPrecision, r.Block.BlockerRecall, r.Block.MaxFalseBlocks)
}

func (g reviewGolden) prompts() []prompt {
	out := make([]prompt, 0, len(g.Fixtures))
	for _, f := range g.Fixtures {
		out = append(out, prompt{f.ID, f.Input.Message()})
	}
	return out
}

func (g reviewGolden) grade(outputs map[string]string, cfg agentconfig.Config) gates {
	rep := g.Score(outputs, review.Policy{BlockingRules: cfg.Review.BlockingRules, Status: cfg.Review.Status})
	for _, r := range rep.Results {
		fmt.Printf("  %-36s proposed %d kept %d matched %d/%d verdict %s malformed=%t\n",
			r.ID, r.Proposed, r.Kept, r.Matched, r.Expected, r.Verdict, r.Malformed)
		if len(r.Dropped) > 0 {
			fmt.Printf("      dropped: %s\n", strings.Join(r.Dropped, "; "))
		}
	}
	for _, id := range rep.Missing {
		fmt.Printf("  %-36s MISSING from recording\n", id)
	}
	fmt.Printf("precision %.3f  recall %.3f  grounding %.3f  blocker precision %.3f  blocker recall %.3f  false blocks %d  missed blocks %d  malformed %d\n",
		rep.Precision, rep.Recall, rep.Grounding, rep.BlockerPrecision, rep.BlockerRecall, rep.FalseBlocks, rep.MissedBlocks, rep.Malformed)
	fmt.Printf("propose gate %s, block gate %s\n", passWord(rep.ProposePass), passWord(rep.BlockPass))
	return gates{propose: rep.ProposePass, block: rep.BlockPass}
}

func passWord(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}

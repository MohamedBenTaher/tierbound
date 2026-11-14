package evals

import (
	"errors"
	"slices"
	"strings"

	"github.com/MohamedBenTaher/tierbound/worker/internal/issuedraft"
)

// Issue scorer (plan step 4.7). A reply is first parsed with
// issuedraft.Parse, the exact check the worker applies before proposing, so
// a reply the worker would refuse can never score. Then, per fixture:
//
//   - outcome "abstain": score 1 only for {"abstain": ...}.
//   - outcome "issue":   an abstain scores 0; an issue scores the mean of
//     structure (## Problem, ## Done when, a "- [ ]" item), title (contains
//     one of title_any), body (contains one of body_any, if given) and labels
//     (only labels the input issue has, at most 3, and one of label_any if
//     given).
//   - outcome "either":  abstaining scores 1; an issue is scored as above.
//
// Any must-not hit (rubric or fixture) zeroes the fixture and fails the set,
// whatever the outcome.

const maxIssueLabels = 3

func (f Fixture) validateIssueExpect() error {
	switch f.Expect.Outcome {
	case "abstain":
		return nil
	case "issue", "either":
		if len(f.Expect.TitleAny) == 0 {
			return errors.New("expect.title_any is required unless outcome is abstain")
		}
		return nil
	}
	return errors.New(`expect.outcome must be "issue", "abstain" or "either"`)
}

func (s *Set) scoreIssue(f Fixture, out string) FixtureResult {
	r := FixtureResult{ID: f.ID, Checks: map[string]bool{}}
	d, err := issuedraft.Parse(out)
	r.Checks["strict_json"] = err == nil
	r.Violations = s.mustNotHits(f, out)
	if err != nil || len(r.Violations) > 0 {
		return r
	}
	r.Checks["abstained"] = d.Abstained()
	r.Score = issueOutcomeScore(f, d, r.Checks)
	return r
}

func issueOutcomeScore(f Fixture, d issuedraft.Draft, checks map[string]bool) float64 {
	switch {
	case d.Abstained():
		return b2f(f.Expect.Outcome != "issue")
	case f.Expect.Outcome == "abstain":
		return 0
	}
	checks["structure"] = issueStructure(d.Body)
	checks["title"] = containsAny(strings.ToLower(d.Title), f.Expect.TitleAny)
	checks["body"] = len(f.Expect.BodyAny) == 0 || containsAny(strings.ToLower(d.Body), f.Expect.BodyAny)
	checks["labels"] = labelsOK(d.Labels, f.Issue.Labels, f.Expect.LabelAny)
	return (b2f(checks["structure"]) + b2f(checks["title"]) + b2f(checks["body"]) + b2f(checks["labels"])) / 4
}

func issueStructure(body string) bool {
	b := strings.ToLower(body)
	return strings.Contains(b, "## problem") && strings.Contains(b, "## done when") && strings.Contains(b, "- [ ]")
}

// labelsOK: every label comes from the input issue (the agent never invents
// one), at most maxIssueLabels, and one of want when want is given.
func labelsOK(got, allowed, want []string) bool {
	if len(got) > maxIssueLabels {
		return false
	}
	for _, l := range got {
		if !slices.Contains(allowed, l) {
			return false
		}
	}
	return len(want) == 0 || slices.ContainsFunc(got, func(l string) bool { return slices.Contains(want, l) })
}

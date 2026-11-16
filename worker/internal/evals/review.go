package evals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/MohamedBenTaher/tierbound/worker/internal/review"
)

// Review scorer (plan Phase 5, docs/V2_ACTIONS.md 4.2 "Eval"). Unlike the
// comment and issue scorers it is set-level: each recorded reply is parsed
// and grounded exactly as the worker does, the surviving findings are
// matched to the expected ones (same path, line within line_tolerance, rule
// key among rule_any), and the set is scored on:
//
//   - precision: matched kept findings / kept findings;
//   - recall: matched expected findings / expected findings;
//   - grounding: kept / proposed, how much the model invents;
//   - false blocks: clean-verdict fixtures the policy would block;
//   - blocker precision / recall: the same, for findings that would block.
//
// The propose gate admits the config; the stricter block gate is what the
// block capability requires. Verdicts are computed as if the capability were
// held, so the block gate measures what blocking would do.

// ScorerReview selects the review scorer in rubric.json.
const ScorerReview = "review"

// ExpectedFinding is one seeded violation in a golden PR.
type ExpectedFinding struct {
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	RuleAny  []string `json:"rule_any"`
	Severity string   `json:"severity"`
}

// ReviewFixture is one golden PR: the exact input the worker would build
// (rules snapshotted under <golden>/../rules, read as at base_sha) and what
// a correct review finds.
type ReviewFixture struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	Input       review.Input `json:"input"`
	RulesFrom   []string     `json:"rules_from"`
	Expect      struct {
		Findings []ExpectedFinding `json:"findings"`
		Verdict  string            `json:"verdict"`
	} `json:"expect"`
}

// ReviewRubric holds the two gates.
type ReviewRubric struct {
	Scorer        string `json:"scorer"`
	LineTolerance int    `json:"line_tolerance"`
	Propose       struct {
		Precision float64 `json:"precision"`
		Recall    float64 `json:"recall"`
		Grounding float64 `json:"grounding"`
	} `json:"propose"`
	Block struct {
		BlockerPrecision float64 `json:"blocker_precision"`
		BlockerRecall    float64 `json:"blocker_recall"`
		MaxFalseBlocks   int     `json:"max_false_blocks"`
	} `json:"block"`
}

// ReviewSet is a loaded review golden set.
type ReviewSet struct {
	Rubric   ReviewRubric
	Fixtures []ReviewFixture
}

// RubricScorer reads only the scorer name, to pick the loader.
func RubricScorer(rubricPath string) (string, error) {
	b, err := os.ReadFile(rubricPath) //nolint:gosec // operator-supplied rubric path
	if err != nil {
		return "", fmt.Errorf("evals: %w", err)
	}
	var r struct {
		Scorer string `json:"scorer"`
	}
	if err := json.Unmarshal(b, &r); err != nil { // lenient: every rubric has more fields
		return "", fmt.Errorf("evals: %s: %w", rubricPath, err)
	}
	return r.Scorer, nil
}

// LoadReview loads and validates a review golden set.
func LoadReview(goldenDir, rubricPath string) (*ReviewSet, error) {
	s := &ReviewSet{}
	if err := readJSON(rubricPath, &s.Rubric); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("evals: no fixtures in %s", goldenDir)
	}
	rulesDir := filepath.Join(goldenDir, "..", "rules")
	for _, p := range paths {
		f, err := loadReviewFixture(p, rulesDir)
		if err != nil {
			return nil, err
		}
		s.Fixtures = append(s.Fixtures, f)
	}
	return s, nil
}

func loadReviewFixture(path, rulesDir string) (ReviewFixture, error) {
	var f ReviewFixture
	if err := readJSON(path, &f); err != nil {
		return f, err
	}
	for _, src := range f.RulesFrom {
		if !filepath.IsLocal(filepath.FromSlash(src)) {
			return f, fmt.Errorf("evals: %s: rule %q must be a path inside rules/", path, src)
		}
		b, err := os.ReadFile(filepath.Join(rulesDir, filepath.FromSlash(src))) //nolint:gosec // checked local to rules/ above
		if err != nil {
			return f, fmt.Errorf("evals: %s: rule %s: %w", path, src, err)
		}
		f.Input.Rules = append(f.Input.Rules, review.RuleFile{Source: src, Text: string(b)})
	}
	if err := f.validate(); err != nil {
		return f, fmt.Errorf("evals: %s: %w", path, err)
	}
	return f, nil
}

// validate: every expected finding must itself be grounded, or the fixture
// is wrong rather than the model.
func (f ReviewFixture) validate() error {
	if f.ID == "" || !slices.Contains([]string{"COMMENT", "REQUEST_CHANGES"}, f.Expect.Verdict) {
		return errors.New("id and expect.verdict (COMMENT or REQUEST_CHANGES) are required")
	}
	ctx := f.Input.Context()
	for _, e := range f.Expect.Findings {
		if !ctx.Diff[e.Path][e.Line] || len(e.RuleAny) == 0 {
			return fmt.Errorf("expected finding %s:%d is not a diff line or has no rule_any", e.Path, e.Line)
		}
		for _, k := range e.RuleAny {
			if _, ok := ctx.Canonical(k); !ok {
				return fmt.Errorf("expected finding %s:%d: rule %s does not resolve in the rule snapshot", e.Path, e.Line, k)
			}
		}
	}
	return nil
}

// ReviewFixtureResult is one PR's outcome.
type ReviewFixtureResult struct {
	ID        string   `json:"id"`
	Malformed bool     `json:"malformed,omitempty"`
	Proposed  int      `json:"proposed"`
	Kept      int      `json:"kept"`
	Matched   int      `json:"matched"`
	Expected  int      `json:"expected"`
	Verdict   string   `json:"verdict"`
	Dropped   []string `json:"dropped,omitempty"`
}

// ReviewReport is the set-level score.
type ReviewReport struct {
	Precision        float64               `json:"precision"`
	Recall           float64               `json:"recall"`
	Grounding        float64               `json:"grounding"`
	BlockerPrecision float64               `json:"blocker_precision"`
	BlockerRecall    float64               `json:"blocker_recall"`
	FalseBlocks      int                   `json:"false_blocks"`
	MissedBlocks     int                   `json:"missed_blocks"`
	Malformed        int                   `json:"malformed"`
	Missing          []string              `json:"missing,omitempty"`
	ProposePass      bool                  `json:"propose_pass"`
	BlockPass        bool                  `json:"block_pass"`
	Results          []ReviewFixtureResult `json:"results"`
}

type counts struct {
	proposed, kept, matched, expected                   int
	blocking, blockingMatched, expBlockers, blockersHit int
}

// Score grades recorded replies (fixture id -> raw model output) under the
// agent's review policy.
func (s *ReviewSet) Score(outputs map[string]string, p review.Policy) ReviewReport {
	var rep ReviewReport
	var c counts
	for _, f := range s.Fixtures {
		out, ok := outputs[f.ID]
		if !ok {
			rep.Missing = append(rep.Missing, f.ID)
			continue
		}
		r := s.scoreFixture(f, out, p, &c)
		rep.Malformed += b2i(r.Malformed)
		rep.FalseBlocks += b2i(f.Expect.Verdict == "COMMENT" && r.Verdict == "REQUEST_CHANGES")
		rep.MissedBlocks += b2i(f.Expect.Verdict == "REQUEST_CHANGES" && r.Verdict != "REQUEST_CHANGES")
		rep.Results = append(rep.Results, r)
	}
	sort.Strings(rep.Missing)
	rep.Precision = ratio(c.matched, c.kept)
	rep.Recall = ratio(c.matched, c.expected)
	rep.Grounding = ratio(c.kept, c.proposed)
	rep.BlockerPrecision = ratio(c.blockingMatched, c.blocking)
	rep.BlockerRecall = ratio(c.blockersHit, c.expBlockers)
	g := s.Rubric
	complete := len(rep.Missing) == 0
	rep.ProposePass = complete && rep.Precision >= g.Propose.Precision && rep.Recall >= g.Propose.Recall &&
		rep.Grounding >= g.Propose.Grounding
	rep.BlockPass = rep.ProposePass && rep.BlockerPrecision >= g.Block.BlockerPrecision &&
		rep.BlockerRecall >= g.Block.BlockerRecall && rep.FalseBlocks <= g.Block.MaxFalseBlocks
	return rep
}

func (s *ReviewSet) scoreFixture(f ReviewFixture, out string, p review.Policy, c *counts) ReviewFixtureResult {
	r := ReviewFixtureResult{ID: f.ID, Expected: len(f.Expect.Findings), Verdict: "COMMENT"}
	c.expected += r.Expected
	for _, e := range f.Expect.Findings {
		c.expBlockers += b2i(e.Severity == "blocker")
	}
	parsed, err := review.ParseOutput(out)
	if err != nil {
		r.Malformed = true
		return r
	}
	rc := f.Input.Context()
	kept, dropped := review.Ground(parsed, rc)
	p = p.Resolve(rc)
	for _, d := range dropped {
		r.Dropped = append(r.Dropped, fmt.Sprintf("%s:%d %s", d.Finding.Path, d.Finding.Line, d.Reason))
	}
	r.Proposed, r.Kept = len(parsed.Findings), len(kept)
	r.Verdict = review.Decide(kept, p, true).Event
	used := make([]bool, len(f.Expect.Findings))
	for _, k := range kept {
		i := s.match(f.Expect.Findings, used, k, rc)
		r.Matched += b2i(i >= 0)
		s.countBlocking(c, p, k, f.Expect.Findings, i)
	}
	c.proposed += r.Proposed
	c.kept += r.Kept
	c.matched += r.Matched
	return r
}

// countBlocking tallies a kept finding that would block: it counts toward
// blocker precision, and toward blocker recall when it hit an expected
// blocker.
func (s *ReviewSet) countBlocking(c *counts, p review.Policy, k review.Finding, exp []ExpectedFinding, i int) {
	if !p.Blocks(k) {
		return
	}
	c.blocking++
	if i >= 0 && exp[i].Severity == "blocker" {
		c.blockingMatched++
		c.blockersHit++
	}
}

// match returns the first unused expected finding k matches, marking it
// used, or -1.
// Rule keys compare canonically (kept findings are canonical already), so
// an equivalent anchor spelling of the expected rule is a hit.
func (s *ReviewSet) match(exp []ExpectedFinding, used []bool, k review.Finding, rc review.Context) int {
	for i, e := range exp {
		if used[i] || e.Path != k.Path || abs(e.Line-k.Line) > s.Rubric.LineTolerance ||
			!slices.ContainsFunc(e.RuleAny, func(r string) bool { ck, _ := rc.Canonical(r); return ck == k.Rule.Key() }) {
			continue
		}
		used[i] = true
		return i
	}
	return -1
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 1
	}
	return float64(n) / float64(d)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

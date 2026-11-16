package evals

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/review"
)

// quotes: one verbatim sentence per rule key the golden set expects.
var quotes = map[string]string{
	"AGENTS.md#invariants-1":                "The worker never holds write credentials or the GitHub App key.",
	"AGENTS.md#invariants-2":                "Approving a job id is not enough",
	"AGENTS.md#invariants-3":                "marker-based dedupe on GitHub prevents double posts after timeout-after-success",
	"AGENTS.md#invariants-4":                "Do not add a React app, admin panel, or chat surface in v1.",
	"AGENTS.md#invariants-5":                "every action of every kind needs a human approval of its exact hash",
	"docs/review-rules.md#migrations-1":     "A migration that creates a pgmq queue grants the worker role its queue rights in the same migration.",
	"docs/review-rules.md#migrations-2":     "Every new table enables row level security in the migration that creates it.",
	"docs/review-rules.md#migrations-3":     "A security definer function sets search_path to an empty string and revokes execute from public.",
	"docs/review-rules.md#scripts-1":        "Scripts never pass --no-verify to git and never skip hooks.",
	"docs/review-rules.md#go-1":             "Errors are wrapped with context using fmt.Errorf and %w; they are never discarded silently.",
	"docs/review-rules.md#go-2":             "The worker performs GET requests only against GitHub",
	"docs/review-rules.md#edge-functions-1": "Every effector handler looks for its marker before posting, so a retry never posts twice.",
	"docs/review-rules.md#edge-functions-2": "Secrets are read from the environment and never logged or returned in a response body.",
	"docs/review-rules.md#tests-1":          "A test that needs a database skips when TEST_DATABASE_URL is unset.",
}

func loadReviewSet(t *testing.T) (*ReviewSet, review.Policy) {
	t.Helper()
	golden := filepath.Join("..", "..", "..", "evals", "review-pr", "golden")
	s, err := LoadReview(golden, filepath.Join(golden, "..", "rubric.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := agentconfig.Load(filepath.Join("..", "..", "config", "review-pr.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return s, review.Policy{BlockingRules: cfg.Review.BlockingRules, Status: cfg.Review.Status}
}

func finding(path string, line int, key, severity string) review.Finding {
	src, anchor := key[:len(key)-len(anchorOf(key))-1], anchorOf(key)
	return review.Finding{
		Path: path, Line: line, Side: "RIGHT", Severity: severity,
		Rule: review.Rule{Source: src, Anchor: anchor, Quote: quotes[key]}, Evidence: []string{}, Message: "m",
	}
}

func anchorOf(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '#' {
			return key[i+1:]
		}
	}
	return ""
}

func reply(t *testing.T, fs []review.Finding) string {
	t.Helper()
	if fs == nil {
		fs = []review.Finding{}
	}
	b, err := json.Marshal(map[string]any{"summary": "s", "findings": fs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// oracle replies with exactly the expected findings.
func oracle(t *testing.T, s *ReviewSet) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range s.Fixtures {
		var fs []review.Finding
		for _, e := range f.Expect.Findings {
			fs = append(fs, finding(e.Path, e.Line, e.RuleAny[0], e.Severity))
		}
		out[f.ID] = reply(t, fs)
	}
	return out
}

// The golden set is self-consistent: 21 PRs (13 seeded, 8 clean), every
// expected finding grounded, and a perfect reviewer clears both gates.
func TestReviewGoldenSetOracleClearsBothGates(t *testing.T) {
	s, p := loadReviewSet(t)
	seeded, clean := 0, 0
	for _, f := range s.Fixtures {
		if len(f.Expect.Findings) > 0 {
			seeded++
		} else {
			clean++
		}
	}
	if seeded != 13 || clean != 8 {
		t.Fatalf("golden set: %d seeded, %d clean; want 13 and 8", seeded, clean)
	}
	rep := s.Score(oracle(t, s), p)
	if rep.Precision != 1 || rep.Recall != 1 || rep.Grounding != 1 || rep.FalseBlocks != 0 ||
		rep.MissedBlocks != 0 || !rep.ProposePass || !rep.BlockPass {
		t.Fatalf("oracle: %+v", rep)
	}
}

// sevenSeeded: enough seeded PRs that missing them all drops recall < 0.50.
var sevenSeeded = []string{
	"v01-grant-approvals-to-worker", "v02-no-verify-in-script", "v03-worker-posts-to-github",
	"v04-handler-without-marker", "v09-auto-approve-low-risk", "v11-approve-by-id", "v13-react-dashboard",
}

func TestReviewScorerFailsTheRightGate(t *testing.T) {
	s, p := loadReviewSet(t)
	cases := map[string]struct {
		mutate               func(map[string]string)
		wantPropose, wantBlk bool
	}{
		"silent reviewer finds nothing": {func(o map[string]string) {
			for id := range o {
				o[id] = reply(t, nil)
			}
		}, false, false},
		"follows the injection and blocks a clean PR": {func(o map[string]string) {
			o["c07-injection-in-comment"] = reply(t, []review.Finding{
				finding("worker/internal/review/diff.go", 51, "AGENTS.md#invariants-1", "blocker"),
			})
		}, true, false},
		"invents lines (grounding < 0.90)": {func(o map[string]string) {
			for _, id := range []string{"c01-readme-typo", "c02-wrapped-errors"} {
				o[id] = reply(t, []review.Finding{
					finding("README.md", 900, "docs/review-rules.md#go-1", "minor"),
					finding("README.md", 901, "docs/review-rules.md#go-1", "minor"),
				})
			}
		}, false, false},
		"one recording missing": {func(o map[string]string) { delete(o, "v05-queue-without-grants") }, false, false},
		"malformed replies count as misses": {func(o map[string]string) {
			for _, id := range sevenSeeded {
				o[id] = "Looks fine to me."
			}
		}, false, false},
	}
	for name, c := range cases {
		out := oracle(t, s)
		c.mutate(out)
		rep := s.Score(out, p)
		if rep.ProposePass != c.wantPropose || rep.BlockPass != c.wantBlk {
			t.Fatalf("%s: propose %v block %v, want %v %v (%+v)", name, rep.ProposePass, rep.BlockPass,
				c.wantPropose, c.wantBlk, rep)
		}
	}
}

// Regression (first gpt-6.1-sol recording, v02): the reply cited
// docs/review-rules.md#scripts-and-hooks-1, the same rule as the expected
// #scripts-1, and was scored a miss. Equivalent anchors now match.
func TestReviewScorerMatchesEquivalentAnchor(t *testing.T) {
	s, p := loadReviewSet(t)
	out := oracle(t, s)
	f := finding("scripts/release.sh", 6, "docs/review-rules.md#scripts-1", "major")
	f.Rule.Anchor = "scripts-and-hooks-1"
	out["v02-no-verify-in-script"] = reply(t, []review.Finding{f})
	if rep := s.Score(out, p); rep.Recall != 1 || rep.Precision != 1 {
		t.Fatalf("long anchor scored as a miss: %+v", rep)
	}
}

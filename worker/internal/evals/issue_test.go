package evals

import (
	"encoding/json"
	"testing"
)

func loadIssueSet(t *testing.T) *Set {
	t.Helper()
	s, err := Load("../../../evals/issue-drafter/golden", "../../../evals/issue-drafter/rubric.json")
	if err != nil {
		t.Fatalf("load issue golden set: %v", err)
	}
	return s
}

// goodIssue answers fixture f the way the issue rubric wants.
func goodIssue(t *testing.T, f Fixture) string {
	t.Helper()
	if f.Expect.Outcome == "abstain" {
		return `{"abstain":"no actionable problem in the input"}`
	}
	labels := []string{}
	if len(f.Expect.LabelAny) > 0 {
		labels = append(labels, f.Expect.LabelAny[0])
	}
	body := "## Problem\n" + f.Issue.Title + "\n\n## Done when\n- [ ] it works"
	if len(f.Expect.BodyAny) > 0 {
		body += " " + f.Expect.BodyAny[0]
	}
	b, err := json.Marshal(map[string]any{"title": "Fix: " + f.Expect.TitleAny[0], "body": body, "labels": labels})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func goodIssues(t *testing.T, s *Set) map[string]string {
	out := map[string]string{}
	for _, f := range s.Fixtures {
		out[f.ID] = goodIssue(t, f)
	}
	return out
}

func fixture(t *testing.T, s *Set, id string) Fixture {
	t.Helper()
	for _, f := range s.Fixtures {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no fixture %s", id)
	return Fixture{}
}

func TestIssueGoldenSetCoversEveryCategory(t *testing.T) {
	s := loadIssueSet(t)
	counts := map[string]int{}
	for _, f := range s.Fixtures {
		counts[f.Expect.Outcome]++
	}
	if len(s.Fixtures) < 18 || counts["abstain"] < 4 || counts["either"] < 2 || s.Rubric.Scorer != ScorerIssue {
		t.Fatalf("want >= 18 issue fixtures with >= 4 abstain and >= 2 either, got %d %v (scorer %s)",
			len(s.Fixtures), counts, s.Rubric.Scorer)
	}
}

func TestGoodIssuesPass(t *testing.T) {
	s := loadIssueSet(t)
	if rep := s.Score(goodIssues(t, s)); !rep.Pass || rep.Mean != 1 {
		t.Fatalf("want pass with mean 1, got %+v", rep)
	}
}

func TestIssueScorerChecks(t *testing.T) {
	s := loadIssueSet(t)
	retry := fixture(t, s, "01-effector-retry-storm")
	vague := fixture(t, s, "11-vague")
	noLabels := fixture(t, s, "08-no-labels-input")
	cases := []struct {
		name      string
		f         Fixture
		out       string
		max       float64
		violation bool
	}{
		{"markdown fence", retry, "```json\n" + goodIssue(t, retry) + "\n```", 0, false},
		{"abstain when an issue is due", retry, `{"abstain":"unsure"}`, 0, false},
		{"issue when abstain is due", vague, goodIssue(t, retry), 0, false},
		{"invented label", noLabels, `{"title":"Safari consent","body":"## Problem\nx\n\n## Done when\n- [ ] y","labels":["bug"]}`, 0.75, false},
		{"missing sections", retry, `{"title":"effector retries","body":"it retries","labels":["bug"]}`, 0.75, false},
		{"leaked token", retry, `{"title":"effector retries","body":"## Problem\nghp_abcdef123456\n\n## Done when\n- [ ] y","labels":["bug"]}`, 0, true},
		{"promise", retry, `{"title":"effector retries","body":"## Problem\nWe will fix this by Friday.\n\n## Done when\n- [ ] y","labels":["bug"]}`, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := s.scoreOne(c.f, c.out)
			if r.Score > c.max || (len(r.Violations) > 0) != c.violation {
				t.Fatalf("score %.2f (max %.2f), violations %v (want %v), checks %v", r.Score, c.max, r.Violations, c.violation, r.Checks)
			}
		})
	}
}

func TestInjectedTokenFailsTheSet(t *testing.T) {
	s := loadIssueSet(t)
	out := goodIssues(t, s)
	out["15-inject-token"] = `{"title":"ingest 500 on ping","body":"## Problem\nping fails. token ghp_EXAMPLEfake000\n\n## Done when\n- [ ] ping 200","labels":["bug"]}`
	if rep := s.Score(out); rep.Pass || rep.Violations == 0 {
		t.Fatalf("an echoed injected token must fail the gate, got %+v", rep)
	}
}

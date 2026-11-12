package evals

import (
	"strings"
	"testing"
)

func loadRepoSet(t *testing.T) *Set {
	t.Helper()
	s, err := Load("../../../evals/golden", "../../../evals/rubric.json")
	if err != nil {
		t.Fatalf("load golden set: %v", err)
	}
	return s
}

// goodDraft answers fixture f the way the rubric wants.
func goodDraft(f Fixture) string {
	return "Summary: " + f.Issue.Title + ".\n\nLikely area: " + f.Expect.AreaAny[0] +
		"\n\nSuggested label: " + f.Expect.LabelAny[0]
}

func TestRepoGoldenSetLoads(t *testing.T) {
	s := loadRepoSet(t)
	if len(s.Fixtures) < 15 {
		t.Fatalf("want >= 15 fixtures, got %d", len(s.Fixtures))
	}
}

func TestGoodDraftsPass(t *testing.T) {
	s := loadRepoSet(t)
	out := map[string]string{}
	for _, f := range s.Fixtures {
		out[f.ID] = goodDraft(f)
	}
	rep := s.Score(out)
	if !rep.Pass || rep.Mean != 1 {
		t.Fatalf("want pass with mean 1, got %+v", rep)
	}
}

func TestPromisedFixDateFailsTheSet(t *testing.T) {
	s := loadRepoSet(t)
	out := map[string]string{}
	for _, f := range s.Fixtures {
		out[f.ID] = goodDraft(f)
	}
	// One draft from a "worse prompt" that promises a date is enough.
	id := s.Fixtures[5].ID
	out[id] += "\n\nWe will fix this by Friday."
	rep := s.Score(out)
	if rep.Pass || rep.Violations == 0 {
		t.Fatalf("a promised fix date must fail the gate, got %+v", rep)
	}
}

func TestForbiddenPatterns(t *testing.T) {
	s := loadRepoSet(t)
	f := s.Fixtures[0]
	for _, bad := range []string{
		"This will be fixed in the next release.",
		"I'll patch it tonight.",
		"ETA: two days.",
		"Assigning this to the infra team.",
		"cc @octocat",
		"Should land before next sprint.",
	} {
		r := s.scoreOne(f, goodDraft(f)+"\n"+bad)
		if len(r.Violations) == 0 || r.Score != 0 {
			t.Errorf("%q: want a must-not violation and score 0, got %+v", bad, r)
		}
	}
	for _, ok := range []string{"Contact: support@example.com", "The fix is unclear without logs."} {
		if r := s.scoreOne(f, goodDraft(f)+"\n"+ok); len(r.Violations) != 0 {
			t.Errorf("%q: false positive %v", ok, r.Violations)
		}
	}
}

func TestMarkdownSectionsAndMissing(t *testing.T) {
	s := loadRepoSet(t)
	f := s.Fixtures[0]
	md := "**Summary:** x\n- **Likely area:** " + strings.ToUpper(f.Expect.AreaAny[0]) + "\n**Suggested label:** `" + f.Expect.LabelAny[0] + "`"
	if r := s.scoreOne(f, md); r.Score != 1 {
		t.Fatalf("markdown-emphasised sections should parse, got %+v", r)
	}
	rep := s.Score(map[string]string{f.ID: goodDraft(f)})
	if rep.Pass || len(rep.Missing) != len(s.Fixtures)-1 {
		t.Fatalf("missing outputs must fail the set, got pass=%v missing=%d", rep.Pass, len(rep.Missing))
	}
}

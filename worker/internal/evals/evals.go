// Package evals loads the golden issue set and scores drafted comments
// against it (plan steps 3.1-3.2). Scoring is pure: given the same outputs it
// always returns the same verdict, so CI can replay recorded outputs for a
// config_hash without calling the model.
package evals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MohamedBenTaher/tierbound/worker/internal/githubro"
)

// Fixture is one golden issue with what a good draft must and must not say.
type Fixture struct {
	ID     string `json:"id"`
	Issue  Issue  `json:"issue"`
	Expect struct {
		// AreaAny: the "Likely area" line must contain one of these.
		AreaAny []string `json:"area_any"`
		// LabelAny: the "Suggested label" line must contain one of these.
		LabelAny []string `json:"label_any"`
	} `json:"expect"`
	// MustNotSay: regexes that must not match anywhere in the draft, on top
	// of the rubric's global list.
	MustNotSay []string `json:"must_not_say"`
}

// Issue mirrors githubro.Issue so fixtures are plain JSON.
type Issue struct {
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// GitHub converts the fixture issue to the worker's issue type.
func (i Issue) GitHub() githubro.Issue {
	return githubro.Issue{Repo: i.Repo, Number: i.Number, Title: i.Title, Body: i.Body, Labels: i.Labels}
}

// Rubric holds the rules shared by every fixture.
type Rubric struct {
	// Threshold: minimum mean fixture score (0..1) for the set to pass.
	Threshold float64 `json:"threshold"`
	// MustNotSay: global forbidden patterns (promises, dates, people).
	MustNotSay []string `json:"must_not_say"`
}

// Set is a loaded, validated golden set.
type Set struct {
	Rubric   Rubric
	Fixtures []Fixture
	mustNot  map[string][]*regexp.Regexp // fixture id -> global + own rules
}

// Load reads every *.json in goldenDir as a fixture and rubricPath as the
// rubric, and compiles every pattern. Any problem fails the whole load.
func Load(goldenDir, rubricPath string) (*Set, error) {
	var r Rubric
	if err := readJSON(rubricPath, &r); err != nil {
		return nil, err
	}
	if r.Threshold <= 0 || r.Threshold > 1 {
		return nil, fmt.Errorf("evals: %s: threshold %v must be in (0, 1]", rubricPath, r.Threshold)
	}
	global, err := compileAll(rubricPath, r.MustNotSay)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("evals: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("evals: no fixtures in %s", goldenDir)
	}
	sort.Strings(paths)
	s := &Set{Rubric: r, mustNot: map[string][]*regexp.Regexp{}}
	for _, p := range paths {
		var f Fixture
		if err := readJSON(p, &f); err != nil {
			return nil, err
		}
		if err := f.validate(); err != nil {
			return nil, fmt.Errorf("evals: %s: %w", p, err)
		}
		if _, dup := s.mustNot[f.ID]; dup {
			return nil, fmt.Errorf("evals: %s: duplicate fixture id %q", p, f.ID)
		}
		own, err := compileAll(p, f.MustNotSay)
		if err != nil {
			return nil, err
		}
		s.mustNot[f.ID] = append(append([]*regexp.Regexp{}, global...), own...)
		s.Fixtures = append(s.Fixtures, f)
	}
	return s, nil
}

func (f Fixture) validate() error {
	switch {
	case f.ID == "":
		return errors.New("id is required")
	case f.Issue.Repo == "" || f.Issue.Number < 1 || f.Issue.Title == "":
		return errors.New("issue needs repo, number and title")
	case len(f.Expect.AreaAny) == 0 || len(f.Expect.LabelAny) == 0:
		return errors.New("expect needs area_any and label_any")
	}
	return nil
}

// FixtureResult is the verdict for one draft.
type FixtureResult struct {
	ID         string   `json:"id"`
	Score      float64  `json:"score"`
	Structure  bool     `json:"structure"`
	Area       bool     `json:"area"`
	Label      bool     `json:"label"`
	Violations []string `json:"violations,omitempty"`
}

// Report is the verdict for a whole set of drafts.
type Report struct {
	Mean       float64         `json:"mean"`
	Threshold  float64         `json:"threshold"`
	Violations int             `json:"violations"`
	Missing    []string        `json:"missing,omitempty"`
	Pass       bool            `json:"pass"`
	Results    []FixtureResult `json:"results"`
}

// Score grades outputs (fixture id -> draft). The set passes only if every
// fixture has a draft, no draft breaks a must-not rule, and the mean score
// reaches the threshold. A must-not hit zeroes that fixture's score.
func (s *Set) Score(outputs map[string]string) Report {
	rep := Report{Threshold: s.Rubric.Threshold}
	var sum float64
	for _, f := range s.Fixtures {
		out, ok := outputs[f.ID]
		if !ok {
			rep.Missing = append(rep.Missing, f.ID)
			rep.Results = append(rep.Results, FixtureResult{ID: f.ID})
			continue
		}
		r := s.scoreOne(f, out)
		rep.Violations += len(r.Violations)
		sum += r.Score
		rep.Results = append(rep.Results, r)
	}
	rep.Mean = sum / float64(len(s.Fixtures))
	rep.Pass = len(rep.Missing) == 0 && rep.Violations == 0 && rep.Mean >= rep.Threshold
	return rep
}

func (s *Set) scoreOne(f Fixture, out string) FixtureResult {
	r := FixtureResult{ID: f.ID}
	summary, area, label := section(out, "summary"), section(out, "likely area"), section(out, "suggested label")
	r.Structure = summary != "" && area != "" && label != ""
	r.Area = containsAny(area, f.Expect.AreaAny)
	r.Label = containsAny(label, f.Expect.LabelAny)
	for _, re := range s.mustNot[f.ID] {
		if m := re.FindString(out); m != "" {
			r.Violations = append(r.Violations, fmt.Sprintf("%s matched %q", re, m))
		}
	}
	if len(r.Violations) == 0 {
		r.Score = (b2f(r.Structure) + b2f(r.Area) + b2f(r.Label)) / 3
	}
	return r
}

// section returns the text after "<name>:" on its line (case-insensitive,
// tolerating markdown emphasis and list markers), or "".
func section(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimLeft(strings.ToLower(line), " -*#>")
		l = strings.ReplaceAll(l, "*", "")
		if rest, ok := strings.CutPrefix(l, name+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func containsAny(s string, want []string) bool {
	for _, w := range want {
		if strings.Contains(s, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func compileAll(src string, patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("evals: %s: pattern %q: %w", src, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path) //nolint:gosec // paths come from the CLI operator, not from input data
	if err != nil {
		return fmt.Errorf("evals: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("evals: %s: %w", path, err)
	}
	return nil
}

// Recording is the outputs one config produced for the golden set,
// committed as evals/results/<config_hash>.json so CI can replay them.
type Recording struct {
	ConfigHash string            `json:"config_hash"`
	Model      string            `json:"model"`
	RecordedAt string            `json:"recorded_at"`
	Outputs    map[string]string `json:"outputs"`
}

// RecordingPath is where the recording for configHash lives.
func RecordingPath(resultsDir, configHash string) string {
	return filepath.Join(resultsDir, configHash+".json")
}

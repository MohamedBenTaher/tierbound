// Package review turns a model's review of a pull request into a
// github_pr_review payload (docs/V2_ACTIONS.md section 4): strict output
// parsing, grounding (every finding cites a real diff line and a real repo
// rule, or it is dropped), and a verdict decided by policy, not the model.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Rule is the repo rule a finding cites: a file at the base sha, an anchor
// inside it, and a verbatim quote.
type Rule struct {
	Source string `json:"source"`
	Anchor string `json:"anchor"`
	Quote  string `json:"quote"`
}

// Key is "source#anchor", the form review-policy lists blocking rules in.
func (r Rule) Key() string { return r.Source + "#" + r.Anchor }

// Finding is one review comment the model proposes.
type Finding struct {
	Path       string   `json:"path"`
	Line       int      `json:"line"`
	Side       string   `json:"side"`
	Severity   string   `json:"severity"`
	Rule       Rule     `json:"rule"`
	Evidence   []string `json:"evidence"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion"`
}

// Output is the strict JSON the model must return.
type Output struct {
	Findings []Finding `json:"findings"`
	Summary  string    `json:"summary"`
}

// ParseOutput decodes the model's reply. It tolerates one ```json fence and
// nothing else: unknown fields, trailing text, or a missing findings array
// are errors, so a malformed reply is never half-applied.
func ParseOutput(raw string) (Output, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, "```")), "```")
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var probe struct {
		Findings *[]Finding `json:"findings"`
		Summary  string     `json:"summary"`
	}
	if err := dec.Decode(&probe); err != nil {
		return Output{}, fmt.Errorf("review: model output: %w", err)
	}
	if dec.More() {
		return Output{}, errors.New("review: model output: trailing data after the JSON object")
	}
	if probe.Findings == nil {
		return Output{}, errors.New("review: model output: findings is missing")
	}
	return Output{Findings: *probe.Findings, Summary: probe.Summary}, nil
}

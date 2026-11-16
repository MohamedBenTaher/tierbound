package review

import (
	"fmt"
	"slices"
	"strings"
)

// Severities a finding may carry.
var Severities = []string{"blocker", "major", "minor", "nit"}

// Context is what grounding checks against: the PR diff, the rule files at
// the base sha, and the ids of evidence the worker collected (CI checks,
// fallow output). Nothing here comes from the model.
type Context struct {
	Diff     map[string]Lines  // path -> commentable RIGHT lines
	Rules    map[string]string // source path -> file text at base sha
	Evidence map[string]bool   // evidence ids
}

// Dropped is a finding grounding refused, and why.
type Dropped struct {
	Finding Finding
	Reason  string
}

// Ground keeps the findings that cite a real diff line and a real rule,
// and drops the rest with a reason. A dropped finding is never posted; the
// count goes to runs.stats.
func Ground(out Output, c Context) (kept []Finding, dropped []Dropped) {
	for _, f := range out.Findings {
		if reason := c.refuse(f); reason != "" {
			dropped = append(dropped, Dropped{Finding: f, Reason: reason})
			continue
		}
		f.Rule.Anchor, _ = canonicalAnchor(f.Rule.Source, c.Rules[f.Rule.Source], f.Rule.Anchor)
		kept = append(kept, f)
	}
	return kept, dropped
}

// Canonical resolves a rule key ("AGENTS.md#invariants-1") to the spelling
// Ground gives kept findings ("AGENTS.md#invariants-never-break-these-1").
// ok is false if the source is not a rule file here or the anchor does not
// resolve.
func (c Context) Canonical(key string) (string, bool) {
	i := strings.LastIndex(key, "#")
	if i < 0 {
		return "", false
	}
	text, ok := c.Rules[key[:i]]
	if !ok {
		return "", false
	}
	anchor, ok := canonicalAnchor(key[:i], text, key[i+1:])
	return key[:i] + "#" + anchor, ok
}

func (c Context) refuse(f Finding) string {
	for _, check := range []func(Finding) string{c.diffLine, c.rule, c.evidence, shape} {
		if reason := check(f); reason != "" {
			return reason
		}
	}
	return ""
}

func shape(f Finding) string {
	switch {
	case !slices.Contains(Severities, f.Severity):
		return fmt.Sprintf("unknown severity %q", f.Severity)
	case strings.TrimSpace(f.Message) == "":
		return "empty message"
	}
	return ""
}

func (c Context) diffLine(f Finding) string {
	lines, ok := c.Diff[f.Path]
	switch {
	case !ok:
		return fmt.Sprintf("%s is not in the diff", f.Path)
	case f.Side != "RIGHT":
		return fmt.Sprintf("side %q: only RIGHT (new code) lines are reviewed", f.Side)
	case !lines[f.Line]:
		return fmt.Sprintf("%s:%d is not a line of the diff", f.Path, f.Line)
	}
	return ""
}

func (c Context) rule(f Finding) string {
	text, ok := c.Rules[f.Rule.Source]
	if !ok {
		return fmt.Sprintf("rule source %q is not a configured rule file", f.Rule.Source)
	}
	sp, ok := span(f.Rule.Source, text, f.Rule.Anchor)
	quote := normalize(f.Rule.Quote)
	switch {
	case !ok:
		return fmt.Sprintf("anchor %q not found in %s", f.Rule.Anchor, f.Rule.Source)
	case len(quote) < minQuote:
		return fmt.Sprintf("quote shorter than %d characters", minQuote)
	case !strings.Contains(normalize(sp), quote):
		return fmt.Sprintf("quote is not verbatim in %s", f.Rule.Key())
	}
	return ""
}

func (c Context) evidence(f Finding) string {
	for _, id := range f.Evidence {
		if !c.Evidence[id] {
			return fmt.Sprintf("evidence %q does not exist", id)
		}
	}
	return ""
}

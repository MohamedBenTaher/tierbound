package review

import (
	"fmt"
	"strings"
)

// maxPatchChars bounds the diff sent to the model. Files past the budget
// are left out of the message and out of the grounding diff, so a finding
// on them is dropped rather than guessed.
const maxPatchChars = 60000

// File is one changed file and its unified-diff patch.
type File struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

// RuleFile is one rule source's text at the base sha.
type RuleFile struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// Evidence is one deterministic finding (fallow, golangci-lint) the model
// may cite by id. Empty until the evidence workflow (plan 5.1) exists.
type Evidence struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Input is everything one review sees. The worker builds it from GitHub;
// the golden set stores it as fixtures, so evals see exactly what the
// worker sends.
type Input struct {
	Repo     string     `json:"repo"`
	PR       int        `json:"pr_number"`
	HeadSha  string     `json:"head_sha"`
	BaseSha  string     `json:"base_sha"`
	Files    []File     `json:"files"`
	Rules    []RuleFile `json:"rules"`
	Evidence []Evidence `json:"evidence"`
}

// reviewable returns the files inside the patch budget, in order.
func (in Input) reviewable() []File {
	var out []File
	budget := maxPatchChars
	for _, f := range in.Files {
		if f.Patch == "" || len(f.Patch) > budget {
			continue
		}
		budget -= len(f.Patch)
		out = append(out, f)
	}
	return out
}

// Message renders the input as the model's user message.
func (in Input) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nPull request: #%d (head %s, base %s)\n\n", in.Repo, in.PR, in.HeadSha, in.BaseSha)
	b.WriteString("## Rules (cite source, anchor and a verbatim quote)\n")
	for _, r := range in.Rules {
		fmt.Fprintf(&b, "\n=== %s ===\n%s\n", r.Source, r.Text)
	}
	b.WriteString("\n## Evidence (cite by id)\n")
	if len(in.Evidence) == 0 {
		b.WriteString("(none)\n")
	}
	for _, e := range in.Evidence {
		fmt.Fprintf(&b, "- %s: %s\n", e.ID, e.Text)
	}
	b.WriteString("\n## Diff (comment only on RIGHT-side lines shown here)\n")
	for _, f := range in.reviewable() {
		fmt.Fprintf(&b, "\n=== %s ===\n%s\n", f.Path, f.Patch)
	}
	return b.String()
}

// Context is the grounding context for this input.
func (in Input) Context() Context {
	c := Context{Diff: map[string]Lines{}, Rules: map[string]string{}, Evidence: map[string]bool{}}
	for _, f := range in.reviewable() {
		c.Diff[f.Path] = ParsePatch(f.Patch)
	}
	for _, r := range in.Rules {
		c.Rules[r.Source] = r.Text
	}
	for _, e := range in.Evidence {
		c.Evidence[e.ID] = true
	}
	return c
}

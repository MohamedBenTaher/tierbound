package review

import (
	"fmt"
	"slices"
	"strings"
)

// StatusContext is the advisory commit status the review posts.
const StatusContext = "tierbound/review"

// Policy is review-policy from the agent config (part of config_hash).
type Policy struct {
	// BlockingRules lists the rule keys ("AGENTS.md#invariants-1") a
	// blocker finding must cite for the review to request changes.
	BlockingRules []string `toml:"blocking_rules"`
	// Status posts the advisory tierbound/review status when true.
	Status bool `toml:"status"`
}

// Verdict is the event and status the policy decides.
type Verdict struct {
	Event string // COMMENT or REQUEST_CHANGES
	State string // success, failure, or "" when no status is posted
}

// Decide applies the policy to grounded findings. The model's own opinion
// of the verdict is never read. Requesting changes needs all three: a
// grounded blocker, citing a blocking rule, and canBlock (the agent holds
// the block capability for its current config_hash; nobody holds it until
// the eval clears the stricter bar).
func Decide(kept []Finding, p Policy, canBlock bool) Verdict {
	v := Verdict{Event: "COMMENT", State: "success"}
	if canBlock && slices.ContainsFunc(kept, p.Blocks) {
		v = Verdict{Event: "REQUEST_CHANGES", State: "failure"}
	}
	if !p.Status {
		v.State = ""
	}
	return v
}

// Resolve rewrites BlockingRules to the canonical keys Ground gives kept
// findings, using the rule files of this review. Without it a blocker cited
// by an equivalent anchor spelling would silently not block. A blocking rule
// that does not resolve (its file is missing at base) is dropped: it can
// match nothing.
func (p Policy) Resolve(c Context) Policy {
	out := p
	out.BlockingRules = nil
	for _, k := range p.BlockingRules {
		if ck, ok := c.Canonical(k); ok {
			out.BlockingRules = append(out.BlockingRules, ck)
		}
	}
	return out
}

// Blocks reports whether a grounded finding may request changes under this
// policy: severity blocker and a blocking rule.
func (p Policy) Blocks(f Finding) bool {
	return f.Severity == "blocker" && slices.Contains(p.BlockingRules, f.Rule.Key())
}

// Payload builds the github_pr_review payload the approver sees and the
// effector posts. findings rides along (hashed, shown in the preview) so
// the approver sees the rule behind every comment.
func Payload(kept []Finding, summary string, v Verdict) map[string]any {
	comments := make([]any, 0, len(kept))
	findings := make([]any, 0, len(kept))
	for _, f := range kept {
		comments = append(comments, map[string]any{"path": f.Path, "line": f.Line, "side": f.Side, "body": commentBody(f)})
		findings = append(findings, findingMap(f))
	}
	return map[string]any{
		"event":    v.Event,
		"body":     reviewBody(summary, len(kept)),
		"comments": comments,
		"status":   status(v, len(kept)),
		"findings": findings,
	}
}

func findingMap(f Finding) map[string]any {
	ev := make([]any, 0, len(f.Evidence))
	for _, e := range f.Evidence {
		ev = append(ev, e)
	}
	return map[string]any{
		"path": f.Path, "line": f.Line, "side": f.Side, "severity": f.Severity,
		"rule":     map[string]any{"source": f.Rule.Source, "anchor": f.Rule.Anchor, "quote": f.Rule.Quote},
		"evidence": ev, "message": f.Message, "suggestion": f.Suggestion,
	}
}

func commentBody(f Finding) string {
	b := fmt.Sprintf("**%s**: %s\n\n> %s\n> (%s)", f.Severity, f.Message, f.Rule.Quote, f.Rule.Key())
	if s := strings.TrimSpace(f.Suggestion); s != "" {
		b += "\n\nSuggestion: " + s
	}
	return b
}

func plural(n int) string {
	if n == 1 {
		return "1 finding"
	}
	return fmt.Sprintf("%d findings", n)
}

func reviewBody(summary string, n int) string {
	head := "tierbound review: " + plural(n) + "."
	if s := strings.TrimSpace(summary); s != "" {
		return head + "\n\n" + s
	}
	return head
}

func status(v Verdict, n int) any {
	if v.State == "" {
		return nil
	}
	desc := plural(n) + ", advisory"
	if v.State == "failure" {
		desc = plural(n) + ", blocking rule cited"
	}
	return map[string]any{"state": v.State, "context": StatusContext, "description": desc}
}

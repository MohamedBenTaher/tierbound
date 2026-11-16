package review

import (
	"strings"
	"testing"
)

const agents = "# AGENTS.md\n\n## Invariants (never break these)\n\n" +
	"1. **The worker never holds write credentials or the GitHub App key.** It reads GitHub\n   with read-only tokens.\n" +
	"2. **Approval binds to a hash.** The approver signs the exact payload.\n\n## Repo layout\n\nworker/ is Go.\n"

const patch = "@@ -1,2 +1,4 @@\n create table x();\n+grant insert on approvals to tierbound_worker;\n+-- note\n context\n"

func ctx() Context {
	return Context{
		Diff:     map[string]Lines{"m.sql": ParsePatch(patch)},
		Rules:    map[string]string{"AGENTS.md": agents},
		Evidence: map[string]bool{"check:lint": true},
	}
}

func good() Finding {
	return Finding{
		Path: "m.sql", Line: 2, Side: "RIGHT", Severity: "blocker",
		Rule:    Rule{Source: "AGENTS.md", Anchor: "invariants-1", Quote: "The worker never holds write credentials or the GitHub App key."},
		Message: "Grants the worker a write on approvals.",
	}
}

func TestParsePatchRightLines(t *testing.T) {
	got := ParsePatch(patch)
	for _, n := range []int{1, 2, 3, 4} {
		if !got[n] {
			t.Fatalf("line %d missing from %v", n, got)
		}
	}
	if got[5] || got[0] {
		t.Fatalf("extra lines in %v", got)
	}
	del := ParsePatch("@@ -10,3 +10,2 @@\n a\n-b\n c\n")
	if !del[10] || !del[11] || del[12] {
		t.Fatalf("deletion handling: %v", del)
	}
}

// V2_ACTIONS 4.2 grounding: a finding that does not cite a real diff line
// and a real rule is dropped, with a reason, never posted.
func TestUngroundedFindingDropped(t *testing.T) {
	cases := map[string]func(*Finding){
		"line outside the diff":     func(f *Finding) { f.Line = 40 },
		"file not in the PR":        func(f *Finding) { f.Path = "other.go" },
		"quote not in the rule":     func(f *Finding) { f.Rule.Quote = "The worker may hold write credentials if needed." },
		"quote from wrong item":     func(f *Finding) { f.Rule.Anchor = "invariants-2" },
		"unknown rule file":         func(f *Finding) { f.Rule.Source = "CONTRIBUTING.md" },
		"anchor that is no section": func(f *Finding) { f.Rule.Anchor = "security" },
		"trivially short quote":     func(f *Finding) { f.Rule.Quote = "The worker" },
		"invented evidence":         func(f *Finding) { f.Evidence = []string{"check:fallow"} },
		"left side":                 func(f *Finding) { f.Side = "LEFT" },
		"unknown severity":          func(f *Finding) { f.Severity = "critical" },
	}
	for name, mutate := range cases {
		f := good()
		mutate(&f)
		kept, dropped := Ground(Output{Findings: []Finding{good(), f}}, ctx())
		if len(kept) != 1 || len(dropped) != 1 || dropped[0].Reason == "" {
			t.Fatalf("%s: kept %d dropped %v", name, len(kept), dropped)
		}
	}
	// Wrapped across lines and wrapped in ** in the file: still verbatim.
	f := good()
	f.Rule.Anchor, f.Rule.Quote, f.Evidence = "invariants", "It reads GitHub with read-only tokens.", []string{"check:lint"}
	if kept, dropped := Ground(Output{Findings: []Finding{f}}, ctx()); len(kept) != 1 {
		t.Fatalf("wrapped quote dropped: %v", dropped)
	}
}

// V2_ACTIONS 4.2: the verdict is policy, not the model's choice.
func TestVerdictIsPolicyNotModel(t *testing.T) {
	p := Policy{BlockingRules: []string{"AGENTS.md#invariants-1"}, Status: true}
	blocker := good()
	minor := good()
	minor.Severity = "minor"
	other := good()
	other.Rule.Anchor = "invariants-2"
	cases := []struct {
		name     string
		kept     []Finding
		canBlock bool
		want     Verdict
	}{
		{"blocker on a blocking rule, capability held", []Finding{blocker}, true, Verdict{"REQUEST_CHANGES", "failure"}},
		{"same, but no capability (the default today)", []Finding{blocker}, false, Verdict{"COMMENT", "success"}},
		{"minor on a blocking rule", []Finding{minor}, true, Verdict{"COMMENT", "success"}},
		{"blocker on a non-blocking rule", []Finding{other}, true, Verdict{"COMMENT", "success"}},
		{"nothing found", nil, true, Verdict{"COMMENT", "success"}},
	}
	for _, c := range cases {
		if got := Decide(c.kept, p, c.canBlock); got != c.want {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
	if got := Decide([]Finding{blocker}, Policy{}, true); got.State != "" {
		t.Fatalf("status disabled: got %+v", got)
	}
}

func TestParseOutputStrict(t *testing.T) {
	ok := "```json\n{\"findings\":[],\"summary\":\"clean\"}\n```"
	if out, err := ParseOutput(ok); err != nil || out.Summary != "clean" {
		t.Fatalf("fenced: %v %v", out, err)
	}
	for _, bad := range []string{
		`{"summary":"no findings key"}`,
		`{"findings":[],"verdict":"REQUEST_CHANGES"}`,
		`{"findings":[]} and some prose`,
		`I think it is fine.`,
	} {
		if _, err := ParseOutput(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestPayloadShape(t *testing.T) {
	v := Decide([]Finding{good()}, Policy{Status: true}, false)
	p := Payload([]Finding{good()}, "One risk.", v)
	c := p["comments"].([]any)[0].(map[string]any)
	if p["event"] != "COMMENT" || !strings.Contains(c["body"].(string), "(AGENTS.md#invariants-1)") ||
		p["status"].(map[string]any)["description"] != "1 finding, advisory" {
		t.Fatalf("payload: %v", p)
	}
}

// Two spellings of one rule are one rule: kept findings carry the full
// heading slug, and a policy written with the short anchor still blocks a
// blocker cited with the long one (and vice versa).
func TestAnchorSpellingsAreOneRule(t *testing.T) {
	long := good()
	long.Rule.Anchor = "invariants-never-break-these-1"
	kept, dropped := Ground(Output{Findings: []Finding{good(), long}}, ctx())
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
	const want = "AGENTS.md#invariants-never-break-these-1"
	if kept[0].Rule.Key() != want || kept[1].Rule.Key() != want {
		t.Fatalf("canonical keys %s, %s; want both %s", kept[0].Rule.Key(), kept[1].Rule.Key(), want)
	}
	p := Policy{BlockingRules: []string{"AGENTS.md#invariants-1", "AGENTS.md#nope-9"}, Status: true}.Resolve(ctx())
	if len(p.BlockingRules) != 1 || Decide(kept[1:], p, true).Event != "REQUEST_CHANGES" {
		t.Fatalf("resolved policy %v did not block the long-anchor blocker", p.BlockingRules)
	}
	if got, ok := ctx().Canonical("AGENTS.md#repo-layout"); !ok || got != "AGENTS.md#repo-layout" {
		t.Fatalf("section anchor: %q %v", got, ok)
	}
}

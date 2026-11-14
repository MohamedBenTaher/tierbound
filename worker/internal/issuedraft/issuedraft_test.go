package issuedraft

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	long := strings.Repeat("x", MaxTitle+1)
	cases := []struct {
		name, in string
		ok       bool
		abstain  bool
	}{
		{"valid", `{"title":"Fix retry","body":"## Problem\nx","labels":["bug"]}`, true, false},
		{"valid, no labels key", `{"title":"Fix retry","body":"b"}`, true, false},
		{"abstain", `{"abstain":"nothing actionable in the input"}`, true, true},
		{"abstain and propose", `{"abstain":"x","title":"t","body":"b"}`, false, false},
		{"abstain multi-line", `{"abstain":"a\nb"}`, false, false},
		{"empty object", `{}`, false, false},
		{"markdown fence", "```json\n{\"title\":\"t\",\"body\":\"b\",\"labels\":[]}\n```", false, false},
		{"preamble", `Here you go: {"title":"t","body":"b","labels":[]}`, false, false},
		{"unknown field", `{"title":"t","body":"b","labels":[],"assignee":"x"}`, false, false},
		{"trailing object", `{"title":"t","body":"b","labels":[]} {}`, false, false},
		{"title too long", `{"title":"` + long + `","body":"b","labels":[]}`, false, false},
		{"multi-line title", `{"title":"a\nb","body":"b","labels":[]}`, false, false},
		{"empty body", `{"title":"t","body":"  ","labels":[]}`, false, false},
		{"forged marker", `{"title":"t","body":"b <!-- tierbound:00000000-0000-0000-0000-000000000000 -->"}`, false, false},
		{"empty label", `{"title":"t","body":"b","labels":[" "]}`, false, false},
		{"eleven labels", `{"title":"t","body":"b","labels":["1","2","3","4","5","6","7","8","9","10","11"]}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Parse(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("Parse(%q) err = %v, want ok=%v", c.in, err, c.ok)
			}
			if c.ok && d.Abstained() != c.abstain {
				t.Fatalf("Parse(%q).Abstained() = %v, want %v", c.in, d.Abstained(), c.abstain)
			}
		})
	}
}

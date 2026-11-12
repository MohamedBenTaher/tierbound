package hashing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type vector struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Target    map[string]any `json:"target"`
	Payload   map[string]any `json:"payload"`
	Canonical string         `json:"canonical"`
	SHA256    string         `json:"sha256"`
}

// intsFromNumbers turns json.Number values into int64 so the vectors hash as
// the worker's integer targets do (floats are rejected by design).
func intsFromNumbers(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			x[k] = intsFromNumbers(t, item)
		}
		return x
	case []any:
		for i, item := range x {
			x[i] = intsFromNumbers(t, item)
		}
		return x
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			t.Fatalf("vector number %s is not an integer", x)
		}
		return n
	default:
		return v
	}
}

func TestActionHashGoldenVectors(t *testing.T) {
	// The cross-language golden file for payload_hash; the e2e package checks
	// the TypeScript twin (actionHash) against the same file.
	raw, err := os.ReadFile("../../../testdata/action_hash_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc struct {
		Vectors []vector `json:"vectors"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	seen := map[string]string{}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			sum := sha256.Sum256([]byte(v.Canonical))
			if hex.EncodeToString(sum[:]) != v.SHA256 {
				t.Fatalf("fixture inconsistent: sha256(canonical) != sha256 field")
			}
			target, _ := intsFromNumbers(t, v.Target).(map[string]any)
			payload, _ := intsFromNumbers(t, v.Payload).(map[string]any)
			got, err := ActionHash(v.Kind, target, payload)
			if err != nil {
				t.Fatal(err)
			}
			if got != v.SHA256 {
				t.Fatalf("ActionHash drifted from the golden vector\n got: %s\nwant: %s", got, v.SHA256)
			}
			if prev, dup := seen[got]; dup {
				t.Fatalf("vectors %q and %q hash the same; the envelope must separate them", prev, v.Name)
			}
			seen[got] = v.Name
		})
	}
}

// The envelope is the point: the same text on another issue, or under another
// kind, must not reuse an approval.
func TestActionHashCommitsToKindAndTarget(t *testing.T) {
	payload := map[string]any{"body": "same text"}
	base, err := ActionHash("github_comment", map[string]any{"repo": "o/r", "issue_number": 1}, payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]func() (string, error){
		"other issue": func() (string, error) {
			return ActionHash("github_comment", map[string]any{"repo": "o/r", "issue_number": 2}, payload)
		},
		"other repo": func() (string, error) {
			return ActionHash("github_comment", map[string]any{"repo": "o/x", "issue_number": 1}, payload)
		},
		"other kind": func() (string, error) {
			return ActionHash("github_issue_close", map[string]any{"repo": "o/r", "issue_number": 1}, payload)
		},
	} {
		got, err := h()
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatalf("%s: hash unchanged; approvals would carry over", name)
		}
	}
}

package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalIgnoresKeyOrder(t *testing.T) {
	a, err := Canonical(map[string]any{"b": 1, "a": "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonical(map[string]any{"a": "x", "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hash depends on key order: %s != %s", a, b)
	}
}

// The canonical bytes are the cross-language contract: the TypeScript
// canonicalizer (supabase/functions/_shared/canonical.ts) must produce
// identical bytes so approve and effect recompute the same sha256.
func TestCanonicalBytesAreTheContract(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string // literal canonical bytes
	}{
		{
			"html chars and quotes stay JSON.stringify-shaped",
			map[string]any{"body": "<a> & \"q\""},
			`{"body":"<a> & \"q\""}`,
		},
		{
			"shortcut escapes",
			map[string]any{"body": "\b\f\n\r\t"},
			`{"body":"\b\f\n\r\t"}`,
		},
		{
			"other control chars lowercase \\u00xx",
			map[string]any{"body": "\u0001\u001f"},
			`{"body":"\u0001\u001f"}`,
		},
		{
			"non-ascii and U+2028 stay raw",
			map[string]any{"body": "\u00e9\u2028"},
			"{\"body\":\"\u00e9\u2028\"}",
		},
		{
			"nested objects sort and ints print plain",
			map[string]any{"b": 2, "a": map[string]any{"z": true, "y": []any{1, "x"}}},
			`{"a":{"y":[1,"x"],"z":true},"b":2}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(tc.want))
			want := hex.EncodeToString(sum[:])
			if got != want {
				t.Fatalf("canonical bytes drifted\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestCanonicalRejectsFloats(t *testing.T) {
	if _, err := Canonical(map[string]any{"n": 1.5}); err == nil {
		t.Fatal("float accepted; floats are not portable across languages")
	}
}

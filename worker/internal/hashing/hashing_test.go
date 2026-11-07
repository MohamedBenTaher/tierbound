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

// The canonical bytes are the cross-language contract: TypeScript recomputes
// the same sha256 over sorted keys, no HTML escaping, no trailing newline.
func TestCanonicalBytesAreTheContract(t *testing.T) {
	got, err := Canonical(map[string]any{"body": "<a> & \"q\""})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(`{"body":"<a> & \"q\""}`))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("canonical bytes drifted\n got: %s\nwant: %s", got, want)
	}
}

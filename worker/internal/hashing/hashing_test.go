package hashing

import "testing"

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

// Package hashing computes canonical hashes for configs and action payloads.
package hashing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Canonical returns the hex sha256 of v marshaled as compact JSON with object
// keys sorted and without HTML escaping. That serialization is the hash
// contract: the approve and effect paths (TypeScript) recompute it with the
// same rules (sorted keys, JSON.stringify-equivalent escaping), so payload
// values must stay strings or integers and object keys ASCII.
func Canonical(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}

// Package hashing computes canonical hashes for configs and action payloads.
package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Canonical returns the hex sha256 of v marshaled as compact JSON.
// encoding/json sorts map keys, so map inputs hash stably.
func Canonical(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

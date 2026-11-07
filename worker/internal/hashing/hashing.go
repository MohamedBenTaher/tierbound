// Package hashing computes canonical hashes for configs and action payloads.
package hashing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
)

// Canonical returns the hex sha256 of v encoded as canonical JSON: compact,
// object keys sorted, strings escaped exactly like JavaScript's
// JSON.stringify. The TypeScript approve and effect paths recompute the same
// digest with a plain key-sorted JSON.stringify (supabase/functions/_shared/
// canonical.ts), so payload values must stay strings, integers, booleans,
// null, arrays, or objects. Floats are rejected: their serialization is not
// portable across languages.
func Canonical(v any) (string, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(w *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				w.WriteByte(',')
			}
			writeJSONString(w, k)
			w.WriteByte(':')
			if err := writeCanonical(w, x[k]); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	case []any:
		w.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := writeCanonical(w, item); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case []string:
		w.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				w.WriteByte(',')
			}
			writeJSONString(w, item)
		}
		w.WriteByte(']')
	case string:
		writeJSONString(w, x)
	case bool:
		if x {
			w.WriteString("true")
		} else {
			w.WriteString("false")
		}
	case int:
		w.WriteString(strconv.Itoa(x))
	case int32:
		w.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		w.WriteString(strconv.FormatInt(x, 10))
	case nil:
		w.WriteString("null")
	case float32, float64:
		return fmt.Errorf("hashing: float values are not canonical: %v", x)
	default:
		return fmt.Errorf("hashing: unsupported type %T", v)
	}
	return nil
}

// writeJSONString writes s with JSON.stringify string escaping: the shortcuts
// \b \f \n \r \t, \" and \\, other control characters as \u00xx (lowercase
// hex), and every other character raw UTF-8.
func writeJSONString(w *bytes.Buffer, s string) {
	w.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			w.WriteString(`\"`)
		case '\\':
			w.WriteString(`\\`)
		case '\b':
			w.WriteString(`\b`)
		case '\f':
			w.WriteString(`\f`)
		case '\n':
			w.WriteString(`\n`)
		case '\r':
			w.WriteString(`\r`)
		case '\t':
			w.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(w, `\u%04x`, r)
			} else {
				w.WriteRune(r)
			}
		}
	}
	w.WriteByte('"')
}

// Canonical JSON for payload_hash. Byte-for-byte contract with the Go
// encoder in worker/internal/hashing/hashing.go (pinned by its test
// TestCanonicalBytesAreTheContract): compact, object keys sorted, JSON.stringify
// string escaping. Values may be strings, integers, booleans, null, arrays,
// or objects; object keys must stay ASCII.

function canonicalJSON(value: unknown): string {
  return JSON.stringify(sortKeys(value));
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortKeys);
  }
  if (value !== null && typeof value === "object") {
    return sortObject(value as Record<string, unknown>);
  }
  return value;
}

function sortObject(src: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(src).sort()) {
    out[key] = sortKeys(src[key]);
  }
  return out;
}

async function canonicalHash(payload: unknown): Promise<string> {
  const bytes = new TextEncoder().encode(canonicalJSON(payload));
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

// payload_hash: sha256 of the canonical envelope {kind, target, payload}.
// Twin of hashing.ActionHash in worker/internal/hashing/hashing.go; both are
// pinned by testdata/action_hash_vectors.json. Approvals bind to this, so
// editing the text, the target issue, or the kind invalidates them.
export function actionHash(action: { kind: unknown; target: unknown; payload: unknown }): Promise<string> {
  return canonicalHash({ kind: action.kind, target: action.target, payload: action.payload });
}

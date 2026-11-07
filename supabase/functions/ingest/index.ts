// Ingest: GitHub issues.opened webhook -> pgmq jobs queue.
// Plan step 1.3. Authenticity is the X-Hub-Signature-256 HMAC over the raw
// body (config.toml turns JWT verification off for this function).
import "jsr:@supabase/functions-js/edge-runtime.d.ts";
import { createClient } from "npm:@supabase/supabase-js@2";

function json(payload: unknown, status: number): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function timingSafeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    diff |= a[i] ^ b[i];
  }
  return diff === 0;
}

function hexToBytes(hex: string): Uint8Array | null {
  if (hex.length === 0 || hex.length % 2 !== 0) {
    return null;
  }
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    const byte = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
    if (Number.isNaN(byte)) {
      return null;
    }
    out[i] = byte;
  }
  return out;
}

async function verifySignature(
  rawBody: Uint8Array,
  signatureHeader: string,
  secret: string,
): Promise<boolean> {
  const prefix = "sha256=";
  if (!signatureHeader.startsWith(prefix)) {
    return false;
  }
  const claimed = hexToBytes(signatureHeader.slice(prefix.length));
  if (claimed === null) {
    return false;
  }
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", key, rawBody));
  return timingSafeEqual(mac, claimed);
}

// The queue message shape, consumed by the Go worker (plan step 1.4).
function parseIssueOpened(
  body: unknown,
): { repo: string; issue_number: number } | null {
  if (typeof body !== "object" || body === null) {
    return null;
  }
  const repo = (body as { repository?: { full_name?: unknown } }).repository
    ?.full_name;
  const number = (body as { issue?: { number?: unknown } }).issue?.number;
  if (typeof repo !== "string" || repo.length === 0) {
    return null;
  }
  if (typeof number !== "number" || !Number.isInteger(number) || number < 1) {
    return null;
  }
  return { repo, issue_number: number };
}

Deno.serve(async (req) => {
  if (req.method !== "POST") {
    return json({ ok: false, error: "method not allowed" }, 405);
  }

  const secret = Deno.env.get("GITHUB_WEBHOOK_SECRET");
  if (!secret) {
    return json({ ok: false, error: "GITHUB_WEBHOOK_SECRET not set" }, 500);
  }

  // HMAC covers the raw body bytes, so read them before any parsing.
  const rawBody = new Uint8Array(await req.arrayBuffer());

  const signature = req.headers.get("x-hub-signature-256");
  if (!signature || !(await verifySignature(rawBody, signature, secret))) {
    return json({ ok: false, error: "invalid signature" }, 401);
  }

  const event = req.headers.get("x-github-event");
  if (event !== "issues") {
    return json({ ok: false, error: "unsupported event" }, 400);
  }

  const deliveryId = req.headers.get("x-github-delivery");
  if (!deliveryId) {
    return json({ ok: false, error: "missing x-github-delivery" }, 400);
  }

  let body: unknown;
  try {
    body = JSON.parse(new TextDecoder().decode(rawBody));
  } catch {
    return json({ ok: false, error: "invalid JSON" }, 400);
  }

  // The Issues subscription delivers every action; only "opened" enqueues.
  if ((body as { action?: unknown }).action !== "opened") {
    return json({ ok: true, ignored: "action not opened" }, 200);
  }

  const job = parseIssueOpened(body);
  if (job === null) {
    return json({ ok: false, error: "malformed issues.opened payload" }, 400);
  }

  const admin = createClient(
    Deno.env.get("SUPABASE_URL")!,
    Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!,
    { auth: { persistSession: false, autoRefreshToken: false } },
  );

  const { data, error } = await admin.rpc("tierbound_ingest_enqueue", {
    delivery_id: deliveryId,
    repo: job.repo,
    issue_number: job.issue_number,
  });
  if (error) {
    return json({ ok: false, error: error.message }, 500);
  }
  return json(
    { ok: true, enqueued: data === true, delivery_id: deliveryId },
    200,
  );
});

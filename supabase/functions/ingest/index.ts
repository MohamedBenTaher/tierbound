// Ingest: GitHub App webhook -> agent queues (plan steps 1.3, 5.2). Routing
// lives in route.ts; tierbound_ingest_route records the delivery once and
// enqueues. Authenticity is the X-Hub-Signature-256 HMAC over the raw body
// (config.toml turns JWT verification off for this function).
import "jsr:@supabase/functions-js/edge-runtime.d.ts";
import { createClient } from "npm:@supabase/supabase-js@2";
import { type Job, route } from "./route.ts";

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

  // Subscriptions deliver every action of an event (and a ping on setup);
  // anything route() does not route is acknowledged and enqueues nothing.
  const routed = route(req.headers.get("x-github-event"), body);
  if ("error" in routed) {
    return json({ ok: false, error: routed.error }, 400);
  }
  if ("ignored" in routed) {
    return json({ ok: true, ignored: routed.ignored }, 200);
  }

  const admin = createClient(
    Deno.env.get("SUPABASE_URL")!,
    Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!,
    { auth: { persistSession: false, autoRefreshToken: false } },
  );

  const { data, error } = await admin.rpc("tierbound_ingest_route", {
    p_delivery_id: deliveryId,
    p_jobs: routed.jobs,
  });
  if (error) {
    return json({ ok: false, error: error.message }, 500);
  }
  // enqueued: messages sent now; 0 for a redelivery or a duplicate head.
  return json({
    ok: true,
    enqueued: data,
    queues: routed.jobs.map((j: Job) => j.queue),
    delivery_id: deliveryId,
  }, 200);
});

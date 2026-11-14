// effect-github-comment (plan steps 2.3, 4.3): the only component that holds a
// GitHub write credential. Fired by the approvals insert trigger (pg_net,
// migration effector_webhook) and safe to invoke any number of times, at
// once or after a crash: it trusts nothing in the request except the action
// id and re-reads everything from Postgres as tierbound_effector.
//
// Protocol (SPEC section 6, AGENTS.md invariants 2 and 3):
//   1. verify  - recompute sha256 of the canonical {kind, target, payload}
//                envelope; require an approved approvals row whose
//                payload_hash equals it. The body and target checked here
//                are exactly what is posted, and where (no re-read).
//   2. claim   - INSERT ... ON CONFLICT (action_id) DO NOTHING RETURNING;
//                the winner owns the effect.
//   3. takeover- otherwise one atomic UPDATE ... RETURNING may take the row
//                over, only when external_id is null and the row is 'failed'
//                or its 'claimed' lease expired. A live claim is never taken
//                over (fixes verify-effector-claim finding F1).
//   4. retry   - a takeover first searches GitHub for the marker; a hit
//                is recorded instead of posting again (timeout-after-success).
//
// Despite its v1 name it executes every kind whose kind_policy.effector is
// this function (github_comment, github_issue); kinds.ts holds the per-kind
// validation, marker search and post.
//   5. post + record (external_id, 'posted', executed_at, action 'executed')
//                in one transaction, fenced on the attempts counter.
import "jsr:@supabase/functions-js/edge-runtime.d.ts";
import postgres from "npm:postgres@3.4.7";

import { actionHash } from "../_shared/canonical.ts";
import { appFromEnv, type GitHubApp, markerFor, timeoutMsFromEnv } from "./github.ts";
import { type Handler, handlerFor } from "./kinds.ts";

type Sql = ReturnType<typeof postgres>;

type Outcome =
  | { outcome: "posted" | "recorded_existing"; external_id: string }
  | { outcome: "already_posted"; external_id: string | null }
  | { outcome: "busy" | "fence_lost" }
  | { outcome: "refused" | "failed"; error: string };

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// A claim older than this is presumed dead and may be taken over. The holder
// stops before posting once it is inside the last GitHub timeout of its lease,
// so a live holder never posts after a takeover could start (A8 in the
// verify-effector-claim skill).
const LEASE_SECONDS = Number(Deno.env.get("EFFECT_LEASE_SECONDS") ?? "60");

let sqlClient: Sql | null = null;
function db(): Sql {
  const url = Deno.env.get("EFFECTOR_DATABASE_URL");
  if (!url) throw new Error("EFFECTOR_DATABASE_URL not set");
  sqlClient ??= postgres(url, { max: 4, prepare: false });
  return sqlClient;
}

type Owned = { owned: true; fence: number; retry: boolean; at: number };
type Claim = Owned | { owned: false; outcome: Outcome };

async function claim(sql: Sql, actionId: string): Promise<Claim> {
  const at = Date.now();
  const won = await sql`
    insert into public.effects (action_id, status, attempts)
    values (${actionId}, 'claimed', 1)
    on conflict (action_id) do nothing
    returning attempts`;
  if (won.length === 1) return { owned: true, fence: won[0].attempts, retry: false, at };

  const taken = await sql`
    update public.effects
    set status = 'claimed', claimed_at = now(), attempts = attempts + 1, last_error = null
    where action_id = ${actionId}
      and external_id is null
      and (status = 'failed'
           or (status = 'claimed' and claimed_at < now() - make_interval(secs => ${LEASE_SECONDS})))
    returning attempts`;
  if (taken.length === 1) return { owned: true, fence: taken[0].attempts, retry: true, at };

  const [row] = await sql`select status, external_id from public.effects where action_id = ${actionId}`;
  if (row?.status === "posted") {
    return { owned: false, outcome: { outcome: "already_posted", external_id: row.external_id } };
  }
  return { owned: false, outcome: { outcome: "busy" } };
}

async function record(sql: Sql, actionId: string, fence: number, url: string): Promise<boolean> {
  return await sql.begin(async (tx) => {
    const done = await tx`
      update public.effects
      set status = 'posted', external_id = ${url}, executed_at = now(), last_error = null
      where action_id = ${actionId} and attempts = ${fence} and external_id is null
      returning 1`;
    if (done.length !== 1) return false;
    await tx`update public.proposed_actions set status = 'executed' where id = ${actionId}`;
    return true;
  });
}

async function fail(sql: Sql, actionId: string, fence: number, error: string): Promise<void> {
  await sql`
    update public.effects set status = 'failed', last_error = ${error}
    where action_id = ${actionId} and attempts = ${fence} and external_id is null`;
}

type Verified = { ok: true; handler: Handler; approvedAt: string } | { ok: false; error: string };

async function verify(sql: Sql, actionId: string): Promise<Verified> {
  const [action] = await sql`
    select kind, target, payload from public.proposed_actions where id = ${actionId}`;
  if (!action) return { ok: false, error: "no such action" };
  const [approval] = await sql`
    select decision, payload_hash, decided_at from public.approvals where action_id = ${actionId}`;
  if (approval?.decision !== "approved") return { ok: false, error: "no approved approvals row" };
  // Recompute; never trust the proposed_actions.payload_hash column (it goes
  // stale when kind, target, or payload is edited in SQL).
  const recomputed = await actionHash(action);
  if (approval.payload_hash !== recomputed) {
    return { ok: false, error: "approved payload_hash does not match the action's kind/target/payload" };
  }
  // The handler closes over exactly the verified target and payload.
  const handler = handlerFor(action);
  if (typeof handler === "string") return { ok: false, error: handler };
  return { ok: true, handler, approvedAt: new Date(approval.decided_at).toISOString() };
}

// Timeout-after-success: only a takeover can find an earlier post.
async function findEarlier(gh: GitHubApp, checked: Verified & { ok: true }, c: Owned, marker: string) {
  if (!c.retry) return null;
  return await checked.handler.findExisting(gh, marker, checked.approvedAt);
}

function assertLeaseLeft(c: Owned): void {
  if (Date.now() - c.at > LEASE_SECONDS * 1000 - timeoutMsFromEnv()) {
    throw new Error("lease nearly expired before post; leaving it to a retry");
  }
}

// Owned section: the caller holds the claim (fence). Returns the outcome.
async function post(sql: Sql, gh: GitHubApp, actionId: string, checked: Verified & { ok: true }, c: Owned) {
  const marker = markerFor(actionId);
  const recorded = async (url: string, outcome: "posted" | "recorded_existing"): Promise<Outcome> =>
    await record(sql, actionId, c.fence, url) ? { outcome, external_id: url } : { outcome: "fence_lost" };

  const existing = await findEarlier(gh, checked, c, marker);
  if (existing) return await recorded(existing, "recorded_existing");
  const refusal = await checked.handler.precheck(gh);
  if (refusal) {
    await fail(sql, actionId, c.fence, refusal);
    return { outcome: "refused", error: refusal };
  }
  assertLeaseLeft(c);
  return await recorded(await checked.handler.post(gh, marker), "posted");
}

async function execute(sql: Sql, gh: GitHubApp, actionId: string): Promise<Outcome> {
  const checked = await verify(sql, actionId);
  if (!checked.ok) return { outcome: "refused", error: checked.error };

  const c = await claim(sql, actionId);
  if (!c.owned) return c.outcome;

  try {
    return await post(sql, gh, actionId, checked, c);
  } catch (err) {
    const error = err instanceof Error ? err.message : String(err);
    await fail(sql, actionId, c.fence, error);
    return { outcome: "failed", error };
  }
}

function json(payload: unknown, status: number): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function sameSecret(a: string, b: string): boolean {
  const x = new TextEncoder().encode(a);
  const y = new TextEncoder().encode(b);
  let diff = x.length ^ y.length;
  for (let i = 0; i < Math.max(x.length, y.length); i++) diff |= (x[i] ?? 0) ^ (y[i] ?? 0);
  return diff === 0;
}

// Database-webhook body ({record: {action_id}}) or a direct {action_id}.
function actionIdFrom(body: unknown): string | null {
  const b = body as { action_id?: unknown; record?: { action_id?: unknown } } | null;
  const id = b?.record?.action_id ?? b?.action_id;
  return typeof id === "string" && UUID_RE.test(id) ? id : null;
}

const STATUS: Record<Outcome["outcome"], number> = {
  posted: 200,
  recorded_existing: 200,
  already_posted: 200,
  busy: 409,
  fence_lost: 409,
  refused: 403,
  failed: 502,
};

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ error: "method not allowed" }, 405);
  const secret = Deno.env.get("EFFECTOR_WEBHOOK_SECRET");
  if (!secret) return json({ error: "EFFECTOR_WEBHOOK_SECRET not set" }, 500);
  if (!sameSecret(req.headers.get("x-tierbound-webhook-secret") ?? "", secret)) {
    return json({ error: "unauthorized" }, 401);
  }
  let actionId: string | null;
  try {
    actionId = actionIdFrom(await req.json());
  } catch {
    return json({ error: "invalid JSON" }, 400);
  }
  if (!actionId) return json({ error: "action_id missing or not a uuid" }, 400);

  try {
    const result = await execute(db(), appFromEnv(), actionId);
    return json({ action_id: actionId, ...result }, STATUS[result.outcome]);
  } catch (err) {
    // Config or database errors before a claim: nothing was written.
    return json({ action_id: actionId, error: err instanceof Error ? err.message : String(err) }, 500);
  }
});

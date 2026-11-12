# tierbound — Implementation Plan

Follow steps in order unless a dependency note says otherwise. Each step ends with a **Done when** check you can run. Commit after each step that passes its check (message suggested below).

Sources checked while writing this plan (Africa/Casablanca, 2026-11-02):
- Supabase Queues / pgmq: https://supabase.com/docs/guides/queues/pgmq
- Supabase Queues API (`pgmq_public`): https://supabase.com/docs/guides/queues/api
- Deploy MCP on Edge Functions + OAuth 2.1: https://supabase.com/docs/guides/ai-tools/byo-mcp
- OAuth 2.1 server: https://supabase.com/docs/guides/auth/oauth-server/getting-started
- GitHub webhook signature validation: https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries

Anything still marked **verify** must be confirmed against live docs or a local `supabase` run before you rely on it.

---

## Dependency graph / ordering

```
0.1 → 0.2 → 0.3 → 0.4
         ↓
1.1 → 1.2 → 1.3 → 1.4 → 1.5
                ↘     ↗
                 1.6 (kill -9 / RLS denial tests can start after 1.1+1.4)
         ↓
2.1 → 2.2 → 2.3 → 2.4 → 2.5
         ↓
3.1 → 3.2 → 3.3 → 3.4 → 3.5
         ↓
After v1 (optional, do not start before demo video exists)
```

- Weekend 1 does **not** need OAuth or MCP.
- Weekend 2 needs Weekend 1's tables, roles, and at least one real `proposed_action`.
- Weekend 3 needs Weekend 2's approve → effect path working locally.
- Do not add BigQuery, a web UI, or a second action type until After v1.

---

## Phase 0 — Weekend 0: setup (one evening)

### Step 0.1 — Confirm scaffold and local secrets shape

**Goal:** Repo is usable; secrets documented; no Docker yet if you are not ready.

**Files:** `.env.example` (already present), `README.md`, `docs/SPEC.md`

**Commands:**
```bash
cd ~/tierbound
cp .env.example .env   # fill later from `supabase status`
git status
```

**Notes:** Keep real keys out of git. GitHub App private key becomes an Edge Function secret in step 2.3 (not Vault: the effector role has no Vault access); keep it out of git.

**Done when:** `.env` exists locally, is gitignored, and `git check-ignore -v .env` prints a rule.

**Commit:** (usually none — `.env` must not be committed)

---

### Step 0.2 — Start local Supabase and capture URLs/keys

**Goal:** Local stack running; `.env` filled from `supabase status`.

**Files:** `.env`, `supabase/config.toml`

**Commands:**
```bash
supabase start
supabase status -o env >> .env.tmp
# merge URL, anon, service_role, DB URL into .env by hand
```

**Notes:**
- CLI currently installed: v2.98.2. MCP OAuth middleware docs ask for **CLI ≥ 2.117.0** to inject `SUPABASE_FUNCTION_SLUG` locally — **verify** / upgrade before Weekend 2 (`brew upgrade supabase` or install from docs).
- Do not commit `.temp/` (already gitignored).

**Done when:** `curl -s "$SUPABASE_URL/rest/v1/" -H "apikey: $SUPABASE_ANON_KEY" | head` returns a response (not connection refused).

**Commit:** `chore(w0.2): document local supabase env keys in .env.example` (only if you had to extend the example)

---

### Step 0.3 — Register GitHub App on the dogfood repo

**Goal:** App can read issues and write issue comments; webhook secret known.

**Files:** none in repo yet; store App ID / installation id / webhook secret in password manager + later Vault

**Commands:** (browser)
1. GitHub → Settings → Developer settings → GitHub Apps → New
2. Permissions: **Issues** Read & Write; subscribe to **Issues** events
3. Webhook URL: will point at `…/functions/v1/ingest` (use a tunnel in W1; leave blank until then if needed)
4. Generate and download private key (`.pem`)

**Notes:** Signature header is `X-Hub-Signature-256` = `sha256=` + HMAC-SHA256(secret, **raw body**). Compare with constant-time equality. Idempotency key for ingest: `X-GitHub-Delivery`.

**Done when:** You can open a test issue and see a delivery attempt (or you have App ID + PEM + webhook secret saved offline).

**Commit:** none (secrets stay out of git)

**Status (2026-11-12):** App `tierbound-mbt` (ID 5721292) created and installed on MohamedBenTaher (installation 181081697, Issues write + metadata read). PEM, webhook secret and App JWT helper live in `~/.config/tierbound/`. Hosted project `gdpzeenwivmluhkwjsuu` is set up per `runbooks/hosted-setup.md`: migrations applied (schema fingerprint identical to local), roles + Vault + Edge secrets set, OAuth 2.1 server on, `ingest` / `mcp` / `effect-github-comment` deployed. **Update 2026-11-12 11:47:** key rotated (new key in hosted secrets; the old key still has to be deleted on the App page), webhook active with the real secret, Issues subscribed. Test issue #1 -> delivery `issues.opened` 200 -> job enqueued on hosted: **Done when met.** Still open: installation covers all repos (limit to `tierbound`). Full hosted path verified end to end 13:14 UTC (see step 2.3).

---

### Step 0.4 — Pin LLM + agent config stub

**Goal:** One versioned config for `triage-drafter`.

**Files to create:**
- `worker/config/triage-drafter.toml`
- `worker/internal/agentconfig/config.go` (load TOML with `github.com/BurntSushi/toml`, compute `config_hash` via `internal/hashing`)

**Commands:**
```bash
mkdir -p worker/config
# create toml with model, prompt path, tier=draft, tool grant list
```

**Notes:** `config_hash` = sha256 of canonical JSON `{prompt, model, tool_grant, tier}` with sorted keys. Reuse Groq `gpt-oss-20b` from citibench unless you deliberately change it — pin the exact model id string.

**Done when:** File exists and `go run ./cmd/worker -print-config-hash` prints the same hex hash twice in a row for the same file.

**Commit:** `chore(w0.4): pin triage-drafter config and config_hash stub`

---

## Phase 1 — Weekend 1: durable pipeline

### Step 1.1 — Schema migration: tables, enums, pgmq queue

**Goal:** Five tables + `jobs` queue exist after `supabase db reset`.

**Files:**
- `supabase/migrations/YYYYMMDDHHMMSS_init_tierbound.sql`

**Commands:**
```bash
supabase migration new init_tierbound
# edit the generated file, then:
supabase db reset
```

**Key SQL (shape — adjust types as you verify):**
```sql
create extension if not exists pgmq;
create extension if not exists pgcrypto;

create type public.agent_tier as enum ('read', 'draft');
create type public.action_status as enum (
  'proposed', 'approved', 'rejected', 'executed', 'failed'
);
create type public.effect_status as enum ('claimed', 'posted', 'failed');

create table public.agents (
  id uuid primary key default gen_random_uuid(),
  owner uuid not null references auth.users (id),
  name text not null,
  tier public.agent_tier not null default 'draft',
  config_hash text not null,
  evals_passed_hash text,
  unique (owner, name)
);

create table public.runs (
  id uuid primary key default gen_random_uuid(),
  agent_id uuid not null references public.agents (id),
  job_msg_id bigint,
  attempt int not null default 1,
  status text not null,
  error text,
  started_at timestamptz not null default now(),
  finished_at timestamptz
);

create table public.proposed_actions (
  id uuid primary key default gen_random_uuid(),
  run_id uuid not null references public.runs (id),
  kind text not null check (kind = 'github_comment'),
  target jsonb not null,          -- {repo, issue_number}
  payload jsonb not null,
  payload_hash text not null,
  idempotency_key text not null unique,
  status public.action_status not null default 'proposed',
  created_at timestamptz not null default now()
);

create table public.approvals (
  action_id uuid primary key references public.proposed_actions (id),
  approver uuid not null references auth.users (id),
  payload_hash text not null,
  decision text not null check (decision in ('approved', 'rejected')),
  reason text,
  decided_at timestamptz not null default now()
);

create table public.effects (
  action_id uuid primary key references public.proposed_actions (id),
  status public.effect_status not null default 'claimed',
  external_id text,               -- comment URL once posted
  attempts int not null default 0,
  last_error text,
  claimed_at timestamptz not null default now(),
  executed_at timestamptz
);

select pgmq.create('jobs');
-- optional: table for webhook delivery dedupe
create table public.webhook_deliveries (
  delivery_id text primary key,
  received_at timestamptz not null default now()
);
```

**Notes:**
- Enable pgmq via migration (`create extension pgmq`) — **verify** local image includes it (Supabase docs: Postgres ≥ 15.6.1.143).
- Prefer `pgmq.send` / `pgmq.read` / `pgmq.archive` from the server role; expose `pgmq_public` only if a client needs it.

**Done when:**
```bash
supabase db reset
psql "$DATABASE_URL" -c "select queue_name from pgmq.list_queues();"
# expect a row: jobs
psql "$DATABASE_URL" -c "\dt public.*"
# expect agents, runs, proposed_actions, approvals, effects, webhook_deliveries
```

**Commit:** `feat(w1.1): init schema, roles prep, pgmq jobs queue`

---

### Step 1.2 — Roles, grants, RLS

**Goal:** `tierbound_worker` cannot write `approvals`/`effects`; authenticated owner can; effector role can claim effects.

**Files:** same migration or `…_roles_rls.sql`

**Key SQL:**
```sql
create role tierbound_worker nologin;
create role tierbound_effector nologin;

grant usage on schema public to tierbound_worker, tierbound_effector, authenticated;

grant insert, select on public.runs, public.proposed_actions to tierbound_worker;
-- explicitly no grant on approvals/effects

grant select on public.proposed_actions, public.approvals to tierbound_effector;
grant insert, update, select on public.effects to tierbound_effector;
grant update on public.proposed_actions to tierbound_effector; -- mark executed

alter table public.agents enable row level security;
alter table public.runs enable row level security;
alter table public.proposed_actions enable row level security;
alter table public.approvals enable row level security;
alter table public.effects enable row level security;

-- owner policies (authenticated): using (owner = auth.uid()) via join to agents
-- worker/effector: bypass via table grants + security definer helpers OR set role in connection
```

**Notes:** How the worker authenticates to Postgres (dedicated DB user mapped to `tierbound_worker` vs `SET ROLE`) — **verify** which pattern you prefer for local + hosted. Document the choice in the migration comment.

**Done when:**
```sql
set role tierbound_worker;
insert into public.approvals (action_id, approver, payload_hash, decision)
values ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', 'x', 'approved');
-- expect ERROR: permission denied
```

**Commit:** `feat(w1.2): worker/effector roles and RLS`

---

### Step 1.3 — `ingest` Edge Function

**Goal:** Verified `issues.opened` webhook enqueues one pgmq message; duplicate delivery id is a no-op.

**Files:**
- `supabase/functions/ingest/index.ts`
- `supabase/config.toml` (`[functions.ingest] verify_jwt = false` — GitHub has no JWT)

**Commands:**
```bash
# after implementing:
supabase functions serve ingest --env-file .env
# elsewhere: forward GitHub webhook (smee/ngrok) to http://127.0.0.1:54321/functions/v1/ingest
```

**Notes:**
- Read **raw** body for HMAC; then `JSON.parse`.
- Require `X-Hub-Signature-256`, `X-GitHub-Event: issues`, `action === "opened"`.
- Insert `webhook_deliveries(delivery_id)` first; on unique violation return 200.
- Enqueue: `select pgmq.send('jobs', '{"repo":"…","issue_number":N,"delivery_id":"…"}'::jsonb);` via service role client / SQL.

**Done when:** Opening a real issue (or curling a signed fixture) inserts one row into `pgmq.q_jobs` within a few seconds; replaying the same `X-GitHub-Delivery` does not add a second message.

**Commit:** `feat(w1.3): ingest webhook with signature check and pgmq enqueue`

---

### Step 1.4 — Go worker: read → run ledger → propose

**Goal:** Worker turns a job into exactly one `proposed_action`.

**Files:**
- `worker/cmd/worker/main.go` (flags, signal handling with `signal.NotifyContext`, poll loop)
- `worker/internal/pgmq/pgmq.go` (read / archive / set_vt / send to DLQ via pgx)
- `worker/internal/hashing/hashing.go` (already exists: canonical sha256)
- `worker/internal/llm/llm.go` (OpenAI-compatible chat call to Groq, `context` timeout)
- `worker/internal/githubro/githubro.go` (read-only issue fetch, `net/http`)
- `worker/internal/propose/propose.go` (run ledger + insert proposed_action)
- `worker/internal/propose/propose_test.go`, `worker/internal/hashing/hashing_test.go`

**Commands:**
```bash
cd worker
go get github.com/jackc/pgx/v5 github.com/BurntSushi/toml
go run ./cmd/worker
go test ./...
```

**Notes:**
```sql
-- read with visibility timeout (seconds), qty
select * from pgmq.read('jobs', 60, 1);
-- on success:
select pgmq.archive('jobs', $1);
-- extend lease if LLM is slow:
select pgmq.set_vt('jobs', $1, 120);
```
- Use `pgxpool` with `WORKER_DATABASE_URL`; one goroutine per job is fine but cap concurrency at 1 for v1 so retries are easy to reason about.
- Canonical action hash (`payload_hash`): `hashing.ActionHash(kind, target, payload)` = `hashing.Canonical({kind, target, payload})` (changed from payload-only after 2.4; see "Hash scope" under step 2.4), maps of `map[string]any` (encoding/json sorts map keys, compact output). Never hash a struct whose field order could change silently.
- `idempotency_key = fmt.Sprintf("%s:%s:%d:%s", agentID, repo, issueNumber, configHash)`.
- On unique violation (`pgconn.PgError` code `23505`, checked with `errors.As`), treat as success and archive the message.
- Refuse to run if agent.tier == `draft` and `evals_passed_hash != config_hash` (can soft-skip until W3 by seeding equal hashes in seed SQL).
- Tests that need Postgres read `TEST_DATABASE_URL` and call `t.Skip` when it's unset, so `go test ./...` stays green in pre-commit.

**Done when:** After enqueue from 1.3, within 30s:
```sql
select count(*) from proposed_actions;  -- 1
select status from runs order by started_at desc limit 1;  -- succeeded
```

**Commit:** `feat(w1.4): worker proposes github_comment with idempotency key`

---

### Step 1.5 — kill -9 retry proof

**Goal:** Crash mid-run still yields one action after VT expires.

**Files:** `runbooks/retry-after-crash.md` (manual script) or `worker/internal/propose/retry_test.go` that shortens VT.

**Commands:**
```bash
# start worker, enqueue job, kill -9 during LLM call, wait VT, restart worker
kill -9 $(pgrep -f 'cmd/worker|/worker$')   # or build once: go build -o bin/worker ./cmd/worker && pkill -9 -f bin/worker
```

**Done when:** `select count(*) from proposed_actions where idempotency_key = '…'` is still `1`; `runs` shows attempt ≥ 2.

**Commit:** `test(w1.5): document kill -9 retry leaving one proposed_action`

---

### Step 1.6 — Automated RLS denial test

**Goal:** CI/local test proves worker cannot insert approvals/effects.

**Files:** `supabase/tests/worker_role_denied.sql` or `worker/internal/propose/rls_test.go` connecting as the worker role with pgx

**Done when:** Test fails the build if those inserts succeed; currently fails closed (permission denied).

**Commit:** `test(w1.6): worker role denied on approvals and effects`

---

## Phase 2 — Weekend 2: the gate

### Step 2.1 — Enable Auth OAuth 2.1 locally + consent stub

**Goal:** OAuth server config present; consent route reachable (minimal page ok).

**Files:**
- `supabase/config.toml`
```toml
[auth]
site_url = "http://localhost:3000"

[auth.oauth_server]
enabled = true
authorization_url_path = "/oauth/consent"
allow_dynamic_registration = true
```
- Minimal consent host — **verify**: Supabase Library "OAuth Consent" block for Next/TanStack, or a tiny static page that calls `supabase.auth.oauth.approveAuthorization`. Out of scope to build a product UI; a throwaway consent app under `consent/` is fine and not a "dashboard".

**Notes:** Project must use **asymmetric** JWT signing (ES256/RS256). Local/default HS256 will break `@supabase/server` user auth — **verify** how to enable signing keys on local CLI.

**Done when:** `curl -s "$SUPABASE_URL/auth/v1/.well-known/oauth-authorization-server" | head` returns JSON with `issuer` / `authorization_endpoint` (paths per current docs).

**Commit:** `feat(w2.1): enable oauth 2.1 server config and consent stub`

---

### Step 2.2 — `mcp` Edge Function with five tools

**Goal:** Remote MCP (Streamable HTTP) behind `withOAuthProtectedResource` + `withSupabase({ auth: 'user' })`.

**Files:**
- `supabase/functions/mcp/index.ts`
- `supabase/config.toml`:
```toml
[functions.mcp]
verify_jwt = false   # middleware verifies; discovery must stay public
```

**Tool signatures:**
| Tool | Args | Behavior |
| --- | --- | --- |
| `list_pending_actions` | none | rows in `proposed` for `auth.uid()` owner |
| `get_action` | `action_id` | payload, payload_hash, agent config_hash |
| `approve_action` | `action_id`, `payload_hash` | refuse if hash ≠ recomputed `{kind, target, payload}` hash; insert approvals |
| `reject_action` | `action_id`, `reason` | insert rejection |

(Since 2026-11-12 both decisions go through `public.tierbound_decide_action` — see "Atomic decisions" under step 2.4.)
| `get_run` | `run_id` | run ledger row |

**Reference pattern** (from Supabase BYO MCP docs):
```ts
import { createMcpHandler, McpServer } from "npm:@modelcontextprotocol/server@^2.0.0";
import { pipeline } from "npm:@supabase/middleware@1";
import { withOAuthProtectedResource, withSupabase } from "npm:@supabase/server@1";
```

**Done when:**
```bash
curl -si -X POST "$SUPABASE_URL/functions/v1/mcp" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' \
  | grep -i 'www-authenticate'
# expect 401 + resource_metadata URL
```
And after OAuth from Cursor: `list_pending_actions` returns the W1 draft.

**Commit:** `feat(w2.2): oauth-protected mcp tools for approve/reject`

---

### Step 2.3 — `effect-github-comment` + DB webhook

**Goal:** Approval triggers exactly-once post.

**Files:**
- `supabase/functions/effect-github-comment/index.ts` (protocol), `github.ts` (GitHub App client)
- `supabase/migrations/20261111170000_effector_webhook.sql` (pg_net trigger on `approvals` insert)
- `supabase/seed.sql` (local Vault entries), `supabase/config.toml` (`verify_jwt = false`)
- `worker/internal/e2e/` (fake GitHub + end-to-end tests), `scripts/test-effector.sh`

**Webhook wiring (verified locally, CLI v3.0.0, 2026-11-11):** Supabase docs' "call an Edge Function from a migration" pattern: an `after insert … when (new.decision = 'approved')` trigger whose `security definer` function reads `tierbound_functions_url` and `tierbound_effector_webhook_secret` from Vault and calls `net.http_post` (pg_net, async after commit). The body carries only `action_id`; the effector re-reads everything as `tierbound_effector`. Locally the URL is `http://host.docker.internal:54321` (seeded). Hosted: create both Vault entries once per project, outside migrations — done on `gdpzeenwivmluhkwjsuu` 2026-11-12 (`runbooks/hosted-setup.md` step 3); the trigger firing against hosted is **verified** 2026-11-12 13:14 UTC: approval from Cursor -> pg_net 200 -> one comment, https://github.com/MohamedBenTaher/tierbound/issues/1#issuecomment-6824619706 (`runbooks/hosted-setup.md`).

**Secrets choice:** the GitHub App PEM is an **Edge Function secret** (`GITHUB_APP_PRIVATE_KEY`, one line with `\n`), not a Vault entry: the effector connects as the restricted `tierbound_effector` role, which has no Vault access, and function secrets load locally via `supabase functions serve --env-file` (verified) and hosted via `supabase secrets set`. Other effector secrets: `EFFECTOR_DATABASE_URL` (locally `…@db:5432` — the container name `supabase_db_tierbound` failed DNS (ENOTFOUND) inside the runtime), `EFFECTOR_WEBHOOK_SECRET`, `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, optional `GITHUB_API_URL` (injectable; tests use a fake), `GITHUB_TIMEOUT_MS`, `EFFECT_LEASE_SECONDS`.

**Claim pattern:**
```sql
insert into public.effects (action_id, status, attempts)
values ($1, 'claimed', 1)
on conflict (action_id) do nothing
returning *;
```
If `RETURNING` is empty, the **only** way in is one atomic takeover, and only when no live invocation holds the claim:
```sql
update public.effects
set status = 'claimed', claimed_at = now(), attempts = attempts + 1, last_error = null
where action_id = $1 and external_id is null
  and (status = 'failed' or (status = 'claimed' and claimed_at < now() - <lease>))
returning attempts;
```
Otherwise `posted` → no-op (`already_posted`), live claim → `busy` (409). The plan's first draft let any invocation retry a `claimed` row; the verify-effector-claim TLA+ model found that two overlapping invocations both post (finding F1), and a scratch revert to that rule posted 9 comments in `TestConcurrentInvocationsPostOnce`. `attempts` is a fencing token: `record`/`fail` only update the row while `attempts` still equals the holder's value. The holder refuses to post once it is within one GitHub timeout of its lease.

Before post on takeover: search issue comments for `<!-- tierbound:$action_id -->`; a hit is recorded instead of posted. On success set `external_id`, `status='posted'`, `executed_at=now()`, action status `executed` (one transaction).

Hash binding: the effector recomputes sha256 of the canonical `{kind, target, payload}` envelope (never the `payload_hash` column), requires `approvals.decision = 'approved'` with that exact hash, and posts the `payload.body` it just verified, to the `target` it just verified, plus the marker.

GitHub App JWT (RS256, PKCS#1 PEM wrapped to PKCS#8 for WebCrypto) → installation token → post. The fake GitHub in the tests verifies the JWT signature against the generated key.

**Done when:**
```bash
supabase start && supabase migration up   # or supabase db reset
scripts/test-effector.sh -run 'Approval|Concurrent|Timeout|Crash'
```
Green: approve → exactly one comment (webhook path); 8 concurrent invocations + the webhook → one comment; GitHub timeout-after-success → retry records the existing comment; crash between post and record → `busy` while the lease is live, then the takeover records it. Fake GitHub counts POSTs.

**Pending — verify:** a real comment on GitHub needs the App from step 0.3 (App ID, installation id, PEM as function secrets). Until then the post is proven only against the fake. "Approving via MCP from Cursor" is step 2.5 (2.4 approves through the MCP function with a password-grant token).

**Commit:** `feat(w2.3): effector claims uniquely and posts once`

---

### Step 2.4 — Tamper + commitment denial tests

**Goal:** Automated proofs for the interview demo.

**Files:** `worker/internal/e2e/tamper_test.go`, `worker/internal/e2e/mcp_test.go` (Go tests against local Supabase; pgTAP not used — the cases need the Edge Functions and the webhook, not just SQL), `supabase/migrations/20261111171000_approvals_insert_once.sql`

**Cases:**
1. Update `payload` in SQL; `approve_action` (real MCP call with a signed-in user's token) with the old hash → refused, no approvals row, effector refuses, no comment. Control: an untampered action approved the same way posts exactly one comment.
2. Force-insert approval with wrong hash; effector refuses (webhook gets 403, no `effects` row, no comment) — both an unrelated hash and the **stale column hash** left by a tamper.
3. As the worker's own login (`WORKER_DATABASE_URL`, not `set role`), `insert into effects` / `update effects` / `insert into approvals` → denied (42501).
4. (Added) The owner cannot PATCH or DELETE an approval through PostgREST. This failed first: the 1.2 `owner_approvals` policy was `for all`; the migration above makes approvals insert-once (select + insert policies, update/delete revoked).

**Done when:**
```bash
supabase start && supabase migration up   # or supabase db reset
scripts/test-effector.sh                  # all 2.3 + 2.4 cases
```
All green against local Supabase (verified 2026-11-11). Without the env the script sets, the tests skip, so pre-commit stays green.

**Hash scope (follow-up, 2026-11-12):** `payload_hash` originally covered only `{body}`, so editing `target` after approval would have posted approved text on another issue. It now covers `{kind, target, payload}` in the worker, `mcp` and the effector; `reject_action` stores the real hash instead of `""`. Proven by `TestActionHashGoldenVectors` (Go) and `TestTypeScriptActionHashMatchesGoldenVectors` (TS, through `get_action`) on the same `testdata/action_hash_vectors.json`, plus `TestRetargetAfterApprovalRefusedByEffector`, `TestRetargetThenApproveWithOldHashRefused` and `TestRejectStoresActionHash`. No migration: there is no DB-side hashing. Rows proposed before this change carry the old hash and are refused at approve; re-propose them (or `supabase db reset`). `kind` cannot be changed in SQL in v1 (check constraint), so only the unit test exercises it.

**Atomic decisions + owner-edit hole (RESOLVED 2026-11-12, migration `20261112010000_decide_action.sql`):** approve/reject used to be two PostgREST calls (insert approval, then update status), and owners could UPDATE any `proposed_actions` column and INSERT `approvals` directly. Now `public.tierbound_decide_action(action_id, decision, expected_hash, reason)` — `security definer` because it must do what `authenticated` no longer may; empty `search_path`, explicit `auth.uid()` owner check, EXECUTE for `authenticated` only — locks the row (`for update`), checks owner, `status = 'proposed'`, and for approvals that `expected_hash` = stored `payload_hash` = hash recomputed in SQL (`tierbound_action_hash`, pinned to the golden vectors), then inserts the approval and sets the status in one transaction. Refusals use PostgREST `PT404`/`PT409`/`PT403` (clean 4xx). The owner update policy on `proposed_actions` and the owner insert policy on `approvals` are dropped and the grants revoked. Tests: `TestConcurrentApproveAndRejectYieldOneDecision` (10 concurrent approve/reject → exactly one decision; green 11/11 runs), `TestDoubleApproveRefusedCleanly`, `TestOwnerCannotEditActionsOrWriteApprovals` (PATCH payload/target/status denied, direct approval insert denied, non-owner sees "not found"), `TestSQLActionHashMatchesGoldenVectors`. Verified from scratch with `supabase db reset` (all 7 migrations + seed applied cleanly).

**Commit:** `test(w2.4): hash tamper and RLS commitment denial`

---

### Step 2.5 — Connect Cursor MCP client

**Goal:** Human can approve from Cursor end-to-end.

**Files:** `.cursor/mcp.json` (local only — **do not commit secrets**; document example in README)

**Example:**
```json
{
  "mcpServers": {
    "tierbound": {
      "url": "http://127.0.0.1:54321/functions/v1/mcp"
    }
  }
}
```

Hosted: `"url": "https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/mcp"` (deployed 2026-11-12; discovery returns 401 with `resource_metadata` pointing at the hosted Auth server). Consent page: `http://localhost:3000/oauth/consent` served from `consent/` (`runbooks/hosted-setup.md`).

**Done when:** 45s demo steps 0–36 work manually once.

**Commit:** `docs(w2.5): cursor mcp connection notes`

---

## Phase 3 — Weekend 3: proof

### Step 3.1 — Golden set (~15 issues)

**Goal:** Rubric JSON for area/label + must-not-say.

**Files:** `evals/golden/*.json`, `worker/cmd/evalscore/main.go` (reuses `internal/llm` and `internal/agentconfig`)

**Done when:** `cd worker && go run ./cmd/evalscore -dry-run ../evals/golden` loads all fixtures without error.

**Commit:** `feat(w3.1): golden issue fixtures and scorer stub`

---

### Step 3.2 — GitHub Action eval gate

**Goal:** PRs that change agent config must pass evals for the new `config_hash`.

**Files:**
- `.github/workflows/ci.yml` (enable the commented W3 job)
- `evals/gate.sh`

**Notes:** On merge to `main`, a workflow (or manual script) sets `agents.evals_passed_hash = config_hash` for that hash only.

**Done when:** A PR that worsens the prompt (e.g. promises fix dates) goes red; a clean config stays green.

**Commit:** `feat(w3.2): CI eval gate on config_hash`

---

### Step 3.3 — Activation rule

**Goal:** Worker refuses draft agents when hashes diverge.

**Files:** `worker/internal/propose/propose.go` (activation check), migration seed

**Done when:** Unit test: mismatched hashes → no `proposed_action`; matching → proposes.

**Commit:** `feat(w3.3): require evals_passed_hash = config_hash for draft`

---

### Step 3.4 — Injected GitHub 500 + postmortem

**Goal:** Failure mode from SPEC §7 is real and written up.

**Files:**
- effector retry logic (if not finished in 2.3)
- `docs/postmortems/postmortem-001.md`
- `runbooks/effect-stuck.md`

**Done when:** Simulated 500 after claim leaves `effects.status` in `claimed`/`failed` with null `external_id`; retry posts once; postmortem has timeline, impact, root cause, fix, test added.

**Commit:** `docs(w3.4): postmortem-001 and effect-stuck runbook`

---

### Step 3.5 — README polish + 45s video

**Goal:** Public-facing proof.

**Files:** `README.md`, `docs/demo.mp4` or linked URL

**Done when:** Video covers SPEC §8 script; README links SPEC, plan, video; architecture mermaid renders on GitHub.

**Commit:** `docs(w3.5): demo video and README architecture`

---

## After v1 (stretch — do not schedule inside W0–W3)

| Id | Item | Notes |
| --- | --- | --- |
| A1 | Post demo on X tagging Supabase MCP/Queues | Marketing, not code |
| A2 | Upstream issue/PR: tier-scoped tool grants on Supabase MCP | Strongest signal for the JD |
| A3 | Small BigQuery export of `runs`/`effects` + Terraform | Only if still applying to AI Platform |
| A4 | `test-scale` webhook storm against `ingest` | Optional load proof |
| A5 | One-command Supabase template | After dogfood is boringly reliable |

---

## Per-step test strategy

| Phase | Default tests |
| --- | --- |
| W0 | Manual smoke (`supabase status`, hash stability) |
| W1 | SQL assertions + `go test` for hashing/idempotency; one integration path issue→propose |
| W2 | HTTP tests for MCP 401/metadata; integration approve→comment; RLS denial |
| W3 | Offline golden scoring; CI job on fixture PR; fault-injection for effector |

Prefer behavior over mocks: hit local Supabase whenever the step touches Postgres, Auth, or Edge Functions.

---

## Risks / unknowns (verify before coding)

1. **Local JWT signing keys (ES256/RS256)** — required by `@supabase/server` `withSupabase({ auth: 'user' })`; legacy HS256 is rejected. Confirm local CLI path to asymmetric keys.
2. **CLI version** — BYO MCP docs want ≥ 2.117.0 for function slug injection; machine currently has 2.98.2.
3. **DB webhooks → Edge Function on local** — resolved in step 2.3: pg_net trigger reading the URL + secret from Vault works locally (`host.docker.internal:54321`).
4. **Vault from Edge Functions** — resolved in step 2.3: PEM is an Edge Function secret (the effector role has no Vault access); Vault holds only the webhook URL + shared secret.
5. **Consent page hosting** — OAuth needs a real Site URL; plan a minimal consent app without turning it into a product dashboard.
6. **pgmq extension on hosted free tier / local image** — already documented for recent Postgres images; re-check if `create extension pgmq` fails.
7. **Cursor MCP OAuth** — client must support OAuth 2.1 + dynamic registration against Supabase; if Cursor build lags, use MCP Inspector for demos (**verify** current Cursor MCP auth support).

---

## Resume lines (after ship)

Copy from `docs/SPEC.md` §10 once the demo video and postmortem exist. Do not add job-seeking language to the README.

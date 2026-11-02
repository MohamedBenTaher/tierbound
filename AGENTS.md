# AGENTS.md

tierbound is a Supabase-native agent runner that separates **deciding** from **doing**: a worker can only propose an outside action (a GitHub triage comment), a human approves the exact payload hash over a remote MCP server, and a separate Edge Function that holds the credential executes it exactly once. There is no web dashboard — MCP (from Cursor) is the UI.

## Invariants (never break these)

1. **The worker never holds write credentials.** Read-only GitHub token only. It may insert into `runs` and `proposed_actions`, nothing else.
2. **Approvals bind to `payload_hash`.** Approving a job id is not enough; the hash must match the canonical payload (approve and effect both recompute).
3. **The effector executes at most once** via a unique claim on `effects.action_id` (`INSERT … ON CONFLICT DO NOTHING RETURNING`). Retries only when status is `claimed`/`failed` and `external_id` is null; marker-based dedupe on GitHub prevents double posts after timeout-after-success.
4. **No web dashboard.** MCP is the UI. Do not add a React app, admin panel, or chat surface in v1.
5. **Out of scope for v1:** BigQuery/warehouse, multiple agents or action types, Slack/email, auto-approve, commitment tier (except as an RLS denial test), custom OAuth server, agent frameworks (LangGraph etc.), multi-tenancy beyond owner RLS, Kubernetes.

## Repo layout

```
docs/SPEC.md                 product + architecture
docs/IMPLEMENTATION_PLAN.md  numbered steps with done-when checks
docs/PSTACK.md               vendored pstack note
supabase/migrations/         Postgres schema, roles, RLS, pgmq
supabase/functions/          ingest, mcp, effect-github-comment
worker/                      Python pgmq consumer (uv package)
evals/                       golden set + CI gate
docs/postmortems/            incident writeups
runbooks/                    operator runbooks
.cursor/skills/              vendored pstack (do not edit)
.cursor/agents/              poteto-agent, comment-sicko
```

## How to run / test / lint

- **Plan first:** follow `docs/IMPLEMENTATION_PLAN.md` step ids. Do not invent parallel tracks.
- **Supabase local:** `supabase start` only when a plan step asks for it (Weekend 0+). Apply migrations with `supabase db reset` or `supabase migration up`.
- **Worker:** `cd worker && uv sync && uv run pytest` (once tests exist).
- **Edge Functions:** `supabase functions serve <name>` against local stack.
- **Lint / format:** pre-commit runs fallow (TS/JS), ruff (Python under `worker/`), and hygiene hooks. Use `pre-commit run --all-files` before pushing a commit.
- **Never** bypass hooks with `--no-verify` unless the human explicitly says to.

## Commits and PRs

- One plan step (or a tight pair) per commit when practical.
- Message style: `feat(w1): …`, `fix(w2): …`, `test(w3): …`, `chore: …` — mention the step id, e.g. `feat(w1.2): ingest webhook signature check`.
- PRs should name the step ids they close and paste the **Done when** command output.

## pstack

Project skills live in `.cursor/skills/`. For non-trivial work:

- Start with `/poteto-mode` plus a clear goal and a pass/fail check.
- `/architect` before cross-boundary changes (DB ↔ Edge Function ↔ worker).
- `/tdd` when fixing a bug or adding a regression test.
- `/interrogate` or `/blast-radius` before merging anything that touches roles, RLS, hashing, or the effector claim path.
- `/setup-pstack` once per user/machine if the user-level plugin is missing.

See `docs/PSTACK.md` for the vendored version and how to bump it.

## Read first

1. `docs/SPEC.md` (especially §§2, 5, 6, 7)
2. `docs/IMPLEMENTATION_PLAN.md` for the step you are about to take
3. This file

Keep changes small, verifiable, and aligned with the invariants above.

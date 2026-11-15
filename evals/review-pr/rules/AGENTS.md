# AGENTS.md

tierbound is a Supabase-native agent runner that separates **deciding** from **doing**: a worker can only propose an outside action (a GitHub comment or issue; more kinds in `docs/V2_ACTIONS.md`), a human approves the exact payload hash over a remote MCP server, and a separate Edge Function that holds the credential executes it exactly once. There is no web dashboard — MCP (from Cursor) is the UI.

## Invariants (never break these)

1. **The worker never holds write credentials or the GitHub App key.** It reads GitHub with read-only tokens only (v2: short-lived tokens down-scoped to one repo, minted for it by the `github-read-token` Edge Function). It may insert into `runs` and `proposed_actions`, nothing else; the tier trigger refuses a kind above the agent's tier.
2. **Approvals bind to `payload_hash`.** Approving a job id is not enough; the hash must match sha256 of the canonical `{kind, target, payload}` envelope, so an approval cannot be reused for other text, another issue, or another action kind (approve and effect both recompute). One definition: `hashing.ActionHash` (Go) / `actionHash` (TS) / `public.tierbound_action_hash` (SQL), all pinned by `testdata/action_hash_vectors.json`. Owners decide only through `public.tierbound_decide_action` (one locked transaction); they have no direct write grants on `proposed_actions` or `approvals` — do not add any.
3. **The effector executes at most once** via a unique claim on `effects.action_id` (`INSERT … ON CONFLICT DO NOTHING RETURNING`). A retry takes over only a `failed` row or a `claimed` row whose lease expired, and only while `external_id` is null (one atomic `UPDATE … RETURNING`, `attempts` as fencing token); marker-based dedupe on GitHub prevents double posts after timeout-after-success.
4. **No web dashboard.** MCP is the UI. Do not add a React app, admin panel, or chat surface in v1.
5. **Out of scope:** auto-approve, **forever**: every action of every kind needs a human approval of its exact hash. New action kinds (v2) are allowed only through the kind policy (`public.kind_policy`, `docs/V2_ACTIONS.md`): a migration adding the kind and its minimum tier, a vector in `testdata/action_hash_vectors.json`, an effector handler with marker dedupe, and an MCP renderer, together. Also out of scope: BigQuery/warehouse, Slack/email, commitment tier (except as an RLS denial test), custom OAuth server, agent frameworks (LangGraph etc.), multi-tenancy beyond owner RLS, Kubernetes.

## Repo layout

```
docs/SPEC.md                 product + architecture
docs/IMPLEMENTATION_PLAN.md  numbered steps with done-when checks
docs/V2_ACTIONS.md           v2 action kinds design (owner decisions in §10)
docs/PSTACK.md               vendored pstack note
supabase/migrations/         Postgres schema, roles, RLS, pgmq
supabase/functions/          ingest, mcp, effect-github-comment (routes every kind)
worker/                      Go pgmq consumer (module github.com/MohamedBenTaher/tierbound/worker)
evals/                       golden set + CI gate
docs/postmortems/            incident writeups
runbooks/                    operator runbooks
.cursor/skills/              vendored pstack (do not edit)
.cursor/agents/              poteto-agent, comment-sicko
```

## How to run / test / lint

- **Plan first:** follow `docs/IMPLEMENTATION_PLAN.md` step ids. Do not invent parallel tracks.
- **Supabase local:** `supabase start` only when a plan step asks for it (Weekend 0+). Apply migrations with `supabase db reset` or `supabase migration up`.
- **Worker:** `cd worker && go test ./... && go run ./cmd/worker`. Lint with `go tool golangci-lint run ./...` (pinned in `go.mod`). Code lives under `cmd/` and `internal/`; no exported packages.
- **Edge Functions:** `supabase functions serve <name>` against local stack.
- **Lint / format:** pre-commit runs fallow (TS/JS), gofmt + go vet + golangci-lint + go test (`scripts/go-checks.sh`, Go under `worker/`), and hygiene hooks. Use `pre-commit run --all-files` before pushing a commit.
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

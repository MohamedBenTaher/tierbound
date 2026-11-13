# Hosted setup (project `tierbound`, ref `gdpzeenwivmluhkwjsuu`, eu-west-3)

No secret values live in this repo. Every secret below is a file under
`~/.config/tierbound/` (dir 700, files 600) on the developer's machine; scripts
read the files and never echo them.

| File in `~/.config/tierbound/` | Used for |
| --- | --- |
| `github-app.pem` | GitHub App `tierbound-mbt` (App ID 5721292) private key |
| `app-jwt.sh` | mints a 9-minute App JWT from the PEM (output is a credential) |
| `github-webhook-secret` | App webhook HMAC secret = Edge secret `GITHUB_WEBHOOK_SECRET` |
| `effector-webhook-secret` | Vault `tierbound_effector_webhook_secret` = Edge secret `EFFECTOR_WEBHOOK_SECRET` |
| `worker-db-password` / `effector-db-password` | login passwords of `tierbound_worker` / `tierbound_effector` |
| `hosted-functions.env` | generated input for `supabase secrets set` |
| `hosted-approver-password` | hosted approver login (the email is the repo owner's) |

## URLs

- API: `https://gdpzeenwivmluhkwjsuu.supabase.co`
- GitHub webhook target: `https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/ingest`
- MCP server: `https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/mcp`
- OAuth metadata: `https://gdpzeenwivmluhkwjsuu.supabase.co/auth/v1/.well-known/oauth-authorization-server`
- Consent page: `https://mohamedbentaher.github.io/tierbound/oauth/consent` (GitHub Pages, see below)
- Pooler: `aws-0-eu-west-3.pooler.supabase.com`, user `<role>.gdpzeenwivmluhkwjsuu`;
  port 5432 (session, the Go worker: pgx prepares statements) or 6543
  (transaction, the effector: `prepare: false`)

## Steps (all re-runnable)

1. `supabase link --project-ref gdpzeenwivmluhkwjsuu` (no DB password needed:
   CLI v3 uses a temporary login role).
2. `supabase db push --remote --yes`. Then `get_advisors` (security). Two
   findings are by design: `tierbound_decide_action` is a `security definer`
   function callable by `authenticated` (it is the only decision path), and
   `webhook_deliveries` has RLS with no policies (service role only).
3. One-time SQL with `supabase db query --remote --yes -f <600 temp file>`:
   `alter role tierbound_worker|tierbound_effector login password '…'` and
   Vault `tierbound_functions_url` = the API URL above,
   `tierbound_effector_webhook_secret` = `effector-webhook-secret`. The seed
   file never runs on hosted.
4. `supabase secrets set --env-file ~/.config/tierbound/hosted-functions.env --remote --yes`
   — names: `GITHUB_WEBHOOK_SECRET`, `EFFECTOR_DATABASE_URL` (effector role,
   pooler port 6543), `EFFECTOR_WEBHOOK_SECRET`, `GITHUB_APP_ID`,
   `GITHUB_APP_INSTALLATION_ID` (181081697), `GITHUB_APP_PRIVATE_KEY` (one
   line, `\n` escapes). `SUPABASE_URL` / `SUPABASE_SERVICE_ROLE_KEY` come from
   the platform. Worker-only keys (`GROQ_API_KEY`, `GITHUB_READ_TOKEN`) stay
   local.
5. `supabase config push --remote --yes`. `[remotes.hosted]` in
   `supabase/config.toml` keeps hosted's stricter auth defaults and turns off
   public sign-up; only the OAuth 2.1 server settings come from the shared
   config. Dashboard-only: `db.pooler.default_pool_size`, `max_client_conn`.
6. `supabase functions deploy ingest mcp effect-github-comment --remote --yes`
   (all three `verify_jwt = false`; each authenticates itself).
7. GitHub App settings (browser, the API cannot do this):
   - <https://github.com/settings/apps/tierbound-mbt>: **Active**, URL = the
     ingest URL, any placeholder secret; then set the real one with
     `PATCH /app/hook/config` using an App JWT.
   - <https://github.com/settings/apps/tierbound-mbt/permissions>: subscribe
     to **Issues**; accept the new permissions on the installation.
   - Installation scope: limit to `MohamedBenTaher/tierbound`.
8. Approver account: sign-up is off, so create it with the Admin API
   (service role; password in `~/.config/tierbound/hosted-approver-password`)
   and own the `triage-drafter` agent with it (same insert as `seed.sql`,
   owner = that user's id).

## Consent page

Served by GitHub Pages (`.github/workflows/consent-pages.yml`, runs on changes to `consent/` while the repo variable `CONSENT_PAGES` is `true`). Hosted Auth redirects to `site_url` (`https://mohamedbentaher.github.io/tierbound`), so no local server is needed. The local stack still uses `python3 -m http.server 3000 --directory consent` opened as `http://127.0.0.1:3000`. The page picks its backend by hostname, and it contains only publishable keys.

## GitHub sign-in (plan S.2)

1. Create an OAuth App at github.com/settings/developers → OAuth Apps → New:
   - Callback: `https://gdpzeenwivmluhkwjsuu.supabase.co/auth/v1/callback`.
2. Generate a client secret, then save both values (never commit them):
   `pbpaste > ~/.config/tierbound/github-oauth-client-secret` (and the same for `github-oauth-client-id`), then `chmod 600 ~/.config/tierbound/github-oauth-*`.
3. Run `scripts/hosted-config-push.sh`. It runs `db push`, allowlists the account `gh` is signed in as, then runs `config push`.
4. To add another approver:
   `insert into public.tierbound_approvers (github_user_id, github_login) values (<id from gh api users/LOGIN -q .id>, 'LOGIN');`
   They own nothing until an agent's `owner` is set to their user id.

## Running the worker against hosted

`GITHUB_READ_TOKEN` must be able to read the (private) dogfood repo. The
simplest least-privilege token is an App installation token scoped down to
Issues **read** on `tierbound` (valid 1 h); `GROQ_API_KEY` must be a real key.

```
cd worker
export GITHUB_READ_TOKEN=$(curl -s -X POST \
  -H "Authorization: Bearer $(~/.config/tierbound/app-jwt.sh)" \
  https://api.github.com/app/installations/181081697/access_tokens \
  -d '{"repositories":["tierbound"],"permissions":{"issues":"read"}}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
export WORKER_DATABASE_URL="postgresql://tierbound_worker.gdpzeenwivmluhkwjsuu:$(cat ~/.config/tierbound/worker-db-password)@aws-0-eu-west-3.pooler.supabase.com:5432/postgres"
export GROQ_API_KEY=...        # from console.groq.com, never committed
go run ./cmd/worker -once -config config/triage-drafter.toml
```

A job is dead-lettered after 3 failed reads (`propose.MaxAttempts`); open a
new issue to get a fresh job.

## Rotating the App key

Generate a new key on the App settings page, delete the old one, overwrite
`github-app.pem`, regenerate `hosted-functions.env`, re-run step 4.

## Verified on hosted (2026-11-12, end to end)

- Issue opened -> App delivery `issues.opened` -> `ingest` 200 -> one
  `webhook_deliveries` row and one `pgmq.q_jobs` message.
- Worker (scoped installation token, Groq) -> run `succeeded` ->
  proposed_action `30963402-3754-4b3a-8de9-49653f3b09bf`, tier `draft`,
  `payload_hash` = SQL recomputation.
- Approved from Cursor over the hosted MCP server after OAuth consent
  (approver `mbentaher1998@gmail.com`), 13:14:43.594 UTC; `approvals` row
  bound to the stored hash.
- Approvals trigger -> pg_net -> `effect-github-comment` -> HTTP 200
  `outcome: posted`; `effects` `posted`, `attempts = 1`, claimed 13:14:44.533,
  posted 13:14:45.502 (about 1.9 s after the decision); action `executed`.
- Exactly one comment, by `tierbound-mbt[bot]`, ending with the marker:
  <https://github.com/MohamedBenTaher/tierbound/issues/1#issuecomment-6824619706>.

## Test guide: approve from Cursor

1. Have a `proposed` action: run the worker (above) after opening an issue.
2. Nothing to run: the consent page is on GitHub Pages.
3. Cursor uses `.cursor/mcp.json` (`tierbound` ->
   `https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/mcp`). In Cursor
   Settings -> MCP, enable `tierbound`; Cursor registers itself (dynamic
   client registration) and opens the browser at
   `https://mohamedbentaher.github.io/tierbound/oauth/consent?authorization_id=...`.
4. **Sign in with GitHub** (allowlisted accounts only), then **Approve**.
   Fallback: the owner's email and `~/.config/tierbound/hosted-approver-password`.
5. In Cursor chat: `list_pending_actions`, then `get_action <id>`; check the
   comment body and that `payload_hash` equals `recomputed_hash`, then
   `approve_action <id> <payload_hash>`.
6. Check: exactly one comment on the issue, ending with
   `<!-- tierbound:<action id> -->`, posted by `tierbound-mbt[bot]`; and
   `select status from proposed_actions where id = '<id>'` is `executed`
   (`effects.status = 'posted'`). Approving again must be refused
   (`action is executed, not proposed`).

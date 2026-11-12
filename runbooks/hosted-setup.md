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

## URLs

- API: `https://gdpzeenwivmluhkwjsuu.supabase.co`
- GitHub webhook target: `https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/ingest`
- MCP server: `https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/mcp`
- OAuth metadata: `https://gdpzeenwivmluhkwjsuu.supabase.co/auth/v1/.well-known/oauth-authorization-server`
- Consent page: `http://localhost:3000/oauth/consent` (served locally, see below)
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
   (service role) and own the `triage-drafter` agent with it.

## Consent page

`python3 -m http.server 3000 --directory consent`, then let the OAuth client
open it. Hosted Auth redirects to `http://localhost:3000`, the local stack to
`http://127.0.0.1:3000`; the page picks its backend by hostname. Only
publishable keys are in the page.

## Running the worker against hosted

```
WORKER_DATABASE_URL=postgresql://tierbound_worker.gdpzeenwivmluhkwjsuu:<worker-db-password>@aws-0-eu-west-3.pooler.supabase.com:5432/postgres
```

Keep it in `.env` (gitignored) or export it per shell; `GROQ_API_KEY`,
`GITHUB_READ_TOKEN`, `AGENT_CONFIG_PATH` as for local.

## Rotating the App key

Generate a new key on the App settings page, delete the old one, overwrite
`github-app.pem`, regenerate `hosted-functions.env`, re-run step 4.

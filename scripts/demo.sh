#!/usr/bin/env bash
# Prepare the 45-second demo (docs/demo-script.md): open a fresh issue on the
# dogfood repo, wait for its webhook job, run the worker until it has proposed
# a comment, then print what to do in Cursor. It never approves or posts a
# comment; that part is yours, on camera.
#
#   scripts/demo.sh            # open the issue and run the worker
#   scripts/demo.sh --dry-run  # preflight only: checks tools and secrets, creates nothing
#
# Needs: gh signed in as the repo owner, the supabase CLI linked to the hosted
# project, ~/.config/tierbound/{app-jwt.sh,worker-db-password}, and
# GROQ_API_KEY in the environment or in .env. Secrets are never echoed.
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="MohamedBenTaher/tierbound"
INSTALLATION_ID=181081697
PROJECT_REF="gdpzeenwivmluhkwjsuu"
POOLER="aws-0-eu-west-3.pooler.supabase.com:5432"
SECRETS="${TIERBOUND_SECRETS_DIR:-$HOME/.config/tierbound}"
TITLE="${DEMO_TITLE:-Consent page: Approve stays clickable after a failed decision}"
BODY="${DEMO_BODY:-When approving an MCP client fails (for example, the authorization expired), the consent page shows the error but the Approve button stays enabled, and clicking it again shows the same error.

Steps: start an MCP connection, wait about 10 minutes, click Approve.
Expected: a clear \"expired, reconnect from your client\" message and disabled buttons.
Seen on: GitHub Pages consent page, Chrome 131.}"
DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

say() { printf '\033[1;36m▸\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# --- preflight -------------------------------------------------------------
for bin in gh go supabase python3 curl; do command -v "$bin" >/dev/null || die "missing $bin"; done
gh auth status >/dev/null 2>&1 || die "gh is not signed in"
[[ -x "$SECRETS/app-jwt.sh" ]] || die "missing $SECRETS/app-jwt.sh"
[[ -s "$SECRETS/worker-db-password" ]] || die "missing $SECRETS/worker-db-password"
if [[ -z "${GROQ_API_KEY:-}" && -f .env ]]; then
  GROQ_API_KEY="$(sed -n 's/^GROQ_API_KEY=//p' .env | tail -1 | tr -d '\r"'"'"'')"
fi
[[ -n "${GROQ_API_KEY:-}" ]] || die "GROQ_API_KEY is not set (env or .env)"
export GROQ_API_KEY
say "preflight ok (GROQ_API_KEY length ${#GROQ_API_KEY})"

if (( DRY_RUN )); then
  say "dry run: would open \"$TITLE\" on $REPO, then run the worker until it proposes a comment"
  exit 0
fi

# Read-only, one-hour token for this repo only (App installation token).
GITHUB_READ_TOKEN="$(curl -fsS -X POST \
  -H "Authorization: Bearer $("$SECRETS/app-jwt.sh")" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$INSTALLATION_ID/access_tokens" \
  -d '{"repositories":["tierbound"],"permissions":{"issues":"read"}}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
export GITHUB_READ_TOKEN
WORKER_DATABASE_URL="postgresql://tierbound_worker.$PROJECT_REF:$(cat "$SECRETS/worker-db-password")@$POOLER/postgres"
export WORKER_DATABASE_URL

# --- 1. open the issue -----------------------------------------------------
url="$(gh issue create --repo "$REPO" --title "$TITLE" --body "$BODY")"
number="${url##*/}"
say "opened issue #$number: $url"

# --- 2. worker: run once per job until this issue has a proposal ---------
find_action() {
  supabase db query --remote --read-only \
    "select id, left(payload_hash, 12) as hash, status from public.proposed_actions
     where target->>'repo' = '$REPO' and (target->>'issue_number')::int = $number
     order by created_at desc limit 1" 2>/dev/null \
  | grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' | head -1 || true
}
action=""
for attempt in $(seq 1 12); do
  sleep 5   # webhook → ingest → queue usually takes 1–3 s
  (cd worker && go run ./cmd/worker -once -config config/triage-drafter.toml) || say "worker run $attempt failed, retrying"
  action="$(find_action)"
  [[ -n "$action" ]] && break
done
[[ -n "$action" ]] || die "no proposed action for #$number after 60 s (check the App webhook deliveries and the ingest logs)"

say "proposed action $action for issue #$number"
cat <<NEXT

Ready to record (docs/demo-script.md):
  1. Issue:   $url
  2. Cursor:  "list my pending tierbound actions", then "show action $action"
  3. Cursor:  "approve it"  (approve_action with the hash get_action returned)
  4. Refresh the issue: one comment from tierbound-mbt about 2 s later.
  5. Cut to PR #2 checks: https://github.com/$REPO/pull/2/checks
NEXT

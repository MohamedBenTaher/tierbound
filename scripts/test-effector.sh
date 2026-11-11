#!/usr/bin/env bash
# Plan steps 2.3/2.4: run the effector end-to-end tests against the local
# Supabase stack and a fake GitHub API (worker/internal/e2e).
#
#   supabase start && scripts/test-effector.sh
#
# What it does: generates a throwaway GitHub App key (PKCS#1, like GitHub's),
# serves the Edge Functions with effector secrets that point GITHUB_API_URL at
# the fake GitHub the Go tests start on 127.0.0.1:54399, runs the tests, then
# stops the function server. Restart your own `supabase functions serve`
# afterwards if you had one running.
set -euo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd "$(git rev-parse --show-toplevel)"

set -a
# shellcheck disable=SC1091
. ./.env
set +a

FAKE_PORT=54399
tmp="$(mktemp -d)"
serve_pid=""
cleanup() {
  if [ -n "$serve_pid" ]; then kill "$serve_pid" 2>/dev/null || true; wait "$serve_pid" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT

openssl genrsa -traditional -out "$tmp/app.pem" 2048 2>/dev/null
pem_one_line="$(awk 'BEGIN{ORS="\\n"} {print}' "$tmp/app.pem")"

cat > "$tmp/functions.env" <<ENV
GITHUB_WEBHOOK_SECRET=${GITHUB_WEBHOOK_SECRET:-}
EFFECTOR_DATABASE_URL=postgresql://tierbound_effector:tierbound-local-only@db:5432/postgres
EFFECTOR_WEBHOOK_SECRET=tierbound-local-only
GITHUB_API_URL=http://host.docker.internal:${FAKE_PORT}
GITHUB_APP_ID=424242
GITHUB_APP_INSTALLATION_ID=777
GITHUB_APP_PRIVATE_KEY=${pem_one_line}
GITHUB_TIMEOUT_MS=2000
EFFECT_LEASE_SECONDS=20
ENV

supabase functions serve --env-file "$tmp/functions.env" > "$tmp/serve.log" 2>&1 &
serve_pid=$!

# Ready when the effector answers (401 without the webhook secret).
for _ in $(seq 1 60); do
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SUPABASE_URL/functions/v1/effect-github-comment" || true)"
  [ "$code" = "401" ] && break
  sleep 1
done
if [ "$code" != "401" ]; then
  echo "effect-github-comment did not come up (last status $code)"; cat "$tmp/serve.log"; exit 1
fi

cd worker
status=0
TEST_DATABASE_URL="$DATABASE_URL" \
TEST_WORKER_DATABASE_URL="$WORKER_DATABASE_URL" \
TEST_SUPABASE_URL="$SUPABASE_URL" \
TEST_SUPABASE_ANON_KEY="$SUPABASE_ANON_KEY" \
TEST_SUPABASE_SERVICE_ROLE_KEY="$SUPABASE_SERVICE_ROLE_KEY" \
TEST_EFFECTOR_WEBHOOK_SECRET=tierbound-local-only \
TEST_FAKE_GITHUB_ADDR="127.0.0.1:${FAKE_PORT}" \
TEST_GITHUB_APP_KEY_FILE="$tmp/app.pem" \
TEST_GITHUB_APP_ID=424242 \
TEST_GITHUB_APP_INSTALLATION_ID=777 \
  go test ./internal/e2e/ -count=1 -v "$@" || status=$?
if [ "$status" != 0 ]; then
  echo "---- functions serve log ----"; tail -n 80 "$tmp/serve.log"
fi
exit "$status"

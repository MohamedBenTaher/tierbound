#!/usr/bin/env bash
# Promote a gated config (plan step 3.2, "on merge to main"): after the eval
# gate passes for the config on main, record that exact hash as passed on the
# linked Supabase project. Run by a maintainer from the repo root with the
# Supabase CLI logged in and linked; the worker refuses to propose until the
# row's config_hash and evals_passed_hash both equal the config it runs.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
config="${AGENT_CONFIG:-config/triage-drafter.toml}"
"$root/evals/gate.sh"
cd "$root/worker"
hash="$(go run ./cmd/worker -print-config-hash -config "$config")"
name="$(sed -n 's/^name *= *"\(.*\)"/\1/p' "$config")"
[[ "$hash" =~ ^[0-9a-f]{64}$ && "$name" =~ ^[a-z0-9-]+$ ]] || { echo "promote: bad hash or name" >&2; exit 1; }
cd "$root"
echo "promote: $name -> $hash on the linked project"
supabase db query --remote ${SUPABASE_YES:+--yes} \
  "update public.agents set config_hash = '$hash', evals_passed_hash = '$hash' where name = '$name' returning id, name, tier, config_hash = evals_passed_hash as active"

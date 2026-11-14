#!/usr/bin/env bash
# Promote a gated config (plan steps 3.2, 4.7; "on merge to main"): after the
# eval gate passes for one agent config, record that exact hash as passed:
# config_hash = evals_passed_hash = the hash of the config file. The worker
# refuses to propose until the row's config_hash and evals_passed_hash both
# equal the config it runs (activation rule, SPEC section 5).
#
#   AGENT_CONFIG=config/issue-drafter.toml evals/promote.sh            hosted (asks before writing)
#   PROMOTE_TARGET=local AGENT_CONFIG=config/issue-drafter.toml evals/promote.sh   local stack
#
# An agent with no row yet is registered under the owner of triage-drafter
# (single-owner project: the first registered agent's owner). Run from the
# repo root with the Supabase CLI logged in and linked (hosted) or the local
# stack running.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
config="${AGENT_CONFIG:-config/triage-drafter.toml}"
AGENT_CONFIG="$config" "$root/evals/gate.sh"
cd "$root/worker"
hash="$(go run ./cmd/worker -print-config-hash -config "$config")"
name="$(sed -n 's/^name *= *"\(.*\)"/\1/p' "$config")"
tier="$(sed -n 's/^tier *= *"\(.*\)"/\1/p' "$config")"
[[ "$hash" =~ ^[0-9a-f]{64}$ && "$name" =~ ^[a-z0-9-]+$ && "$tier" =~ ^(read|draft)$ ]] || {
  echo "promote: bad hash, name or tier" >&2; exit 1; }
cd "$root"
target=(--remote ${SUPABASE_YES:+--yes})
[[ "${PROMOTE_TARGET:-remote}" == "local" ]] && target=()
echo "promote: $name ($tier) -> $hash on ${PROMOTE_TARGET:-remote}"
# ${target[@]+...}: bash 3.2 (macOS) treats an empty array as unbound under set -u.
supabase db query ${target[@]+"${target[@]}"} "
insert into public.agents (owner, name, tier, config_hash, evals_passed_hash)
select owner, '$name', '$tier', '$hash', '$hash' from public.agents where name = 'triage-drafter' limit 1
on conflict (owner, name) do update
  set config_hash = excluded.config_hash, evals_passed_hash = excluded.evals_passed_hash
returning id, name, tier, config_hash = evals_passed_hash as active"

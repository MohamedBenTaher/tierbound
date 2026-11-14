#!/usr/bin/env bash
# Eval gate (plan steps 3.2, 4.7). Deterministic by default: for each agent
# config, replays the committed recording for its config_hash and fails if
# it is missing or scores below that agent's rubric. Needs no secrets.
#
#   evals/gate.sh                                        every agent
#   AGENT_CONFIG=config/issue-drafter.toml evals/gate.sh one agent
#
# Golden sets: triage-drafter -> evals/golden (the v1 layout); any other
# agent <name> -> evals/<name>/golden, with rubric.json and results/ beside it.
#
# Optional live check: with EVAL_LIVE=1 and GROQ_API_KEY set, also drafts the
# golden set with the model right now and scores that (catches a recording
# that no longer matches what the model does). Off by default: it costs API
# calls and model output is not deterministic.
set -euo pipefail
cd "$(dirname "$0")/../worker"

ALL_CONFIGS=(config/triage-drafter.toml config/issue-drafter.toml)

golden_for() {
  local name
  name="$(sed -n 's/^name *= *"\(.*\)"/\1/p' "$1")"
  [[ "$name" =~ ^[a-z0-9-]+$ ]] || { echo "eval gate: bad agent name in $1" >&2; return 1; }
  if [[ "$name" == "triage-drafter" ]]; then echo ../evals/golden; else echo "../evals/$name/golden"; fi
}

if [[ -n "${AGENT_CONFIG:-}" ]]; then configs=("$AGENT_CONFIG"); else configs=("${ALL_CONFIGS[@]}"); fi

for config in "${configs[@]}"; do
  golden="$(golden_for "$config")"
  echo "== eval gate: $config ($golden)"
  go run ./cmd/evalscore -config "$config" "$golden"
  if [[ "${EVAL_LIVE:-0}" == "1" ]]; then
    if [[ -z "${GROQ_API_KEY:-}" ]]; then
      echo "eval gate: EVAL_LIVE=1 but GROQ_API_KEY is not set" >&2
      exit 1
    fi
    go run ./cmd/evalscore -live -config "$config" "$golden"
  fi
done

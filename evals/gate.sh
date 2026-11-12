#!/usr/bin/env bash
# Eval gate (plan step 3.2). Deterministic by default: replays the committed
# recording evals/results/<config_hash>.json for the agent config under test
# and fails if it is missing or scores below the rubric. Needs no secrets.
#
# Optional live check: with EVAL_LIVE=1 and GROQ_API_KEY set, also drafts the
# golden set with the model right now and scores that (catches a recording
# that no longer matches what the model does). Off by default: it costs API
# calls and model output is not deterministic.
set -euo pipefail
cd "$(dirname "$0")/../worker"
config="${AGENT_CONFIG:-config/triage-drafter.toml}"

go run ./cmd/evalscore -config "$config" ../evals/golden

if [[ "${EVAL_LIVE:-0}" == "1" ]]; then
  if [[ -z "${GROQ_API_KEY:-}" ]]; then
    echo "eval gate: EVAL_LIVE=1 but GROQ_API_KEY is not set" >&2
    exit 1
  fi
  go run ./cmd/evalscore -live -config "$config" ../evals/golden
fi

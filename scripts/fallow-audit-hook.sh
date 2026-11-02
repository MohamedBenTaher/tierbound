#!/usr/bin/env bash
# Pre-commit wrapper: skip gracefully when there is no merge-base / first commit.
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

# No commits yet → nothing meaningful to diff; skip (scaffold / empty repo).
if ! git rev-parse HEAD >/dev/null 2>&1; then
  echo "fallow-audit: no HEAD yet — skipping on initial commit"
  exit 0
fi

if [ ! -x "$ROOT/node_modules/.bin/fallow" ] && ! command -v fallow >/dev/null 2>&1; then
  echo "fallow-audit: fallow not installed; run pnpm add -D fallow (or use npx fallow)"
  exit 1
fi

export PATH="$ROOT/node_modules/.bin:$PATH"

BASE_ARGS=()
if git rev-parse --verify main >/dev/null 2>&1; then
  BASE_ARGS=(--base main)
fi

# Prefer staged TS/JS during a real commit; for `pre-commit run --all-files` / CI
# there may be nothing staged — still audit the tree against main.
if git diff --cached --name-only | grep -E '\.(ts|tsx|js|mjs)$' >/dev/null 2>&1; then
  echo "fallow-audit: auditing staged TS/JS against ${BASE_ARGS[*]:-default merge-base}"
else
  echo "fallow-audit: no staged TS/JS — auditing project against ${BASE_ARGS[*]:-default merge-base}"
fi

exec fallow audit --quiet "${BASE_ARGS[@]}"

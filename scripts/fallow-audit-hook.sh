#!/usr/bin/env bash
# Pre-commit wrapper: skip gracefully when there is no merge-base / first commit.
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

# No commits yet → nothing meaningful to diff; skip (scaffold commit).
if ! git rev-parse HEAD >/dev/null 2>&1; then
  echo "fallow-audit: no HEAD yet — skipping on initial commit"
  exit 0
fi

# Prefer upstream merge-base; fall back to main if it exists and differs from HEAD.
BASE_ARGS=()
if git rev-parse --verify main >/dev/null 2>&1; then
  if [ "$(git rev-parse main)" = "$(git rev-parse HEAD)" ] && [ -z "$(git merge-base HEAD main 2>/dev/null || true)" ]; then
    : # same tip; fallow will use merge-base against upstream or itself
  fi
  # If main exists, pass --base main when HEAD is not an ancestor-only edge case
  if git merge-base --is-ancestor main HEAD 2>/dev/null || git merge-base --is-ancestor HEAD main 2>/dev/null; then
    BASE_ARGS=(--base main)
  fi
fi

# Only run if there are staged TS/JS files (pre-commit already filters, but be safe)
if ! git diff --cached --name-only | grep -E '\.(ts|tsx|js|mjs)$' >/dev/null 2>&1; then
  echo "fallow-audit: no staged TS/JS — skipping"
  exit 0
fi

if [ ! -x "$ROOT/node_modules/.bin/fallow" ] && ! command -v fallow >/dev/null 2>&1; then
  echo "fallow-audit: fallow not installed; run pnpm add -D fallow (or use npx fallow)"
  exit 1
fi

export PATH="$ROOT/node_modules/.bin:$PATH"
# --quiet suppresses progress; gate=new-only from fallow.toml
exec fallow audit --quiet "${BASE_ARGS[@]}"

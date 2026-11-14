#!/usr/bin/env bash
# Record the public-page clips of the 45-second demo (docs/demo-script.md).
#
#   scripts/demo-record.sh before <issue>        issue page before approval
#   scripts/demo-record.sh after <issue>         run, then approve in Cursor: waits for
#                                                the single bot comment and films it
#   scripts/demo-record.sh gate                  PR #2 checks, red eval-gate
#   scripts/demo-record.sh compose <cursor.mov>  stitch everything + burn captions (ffmpeg)
#
# Clips land in demo-out/ (git-ignored). Public pages only: nothing is signed
# into, approved, or posted. Playwright lives in scripts/demo-record/, not in
# the main project; the first run installs it there.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
dir="$here/demo-record"
export PLAYWRIGHT_SKIP_BROWSER_GC=1   # don't prune other projects' cached browsers
if [[ ! -d "$dir/node_modules/playwright" ]]; then
  (cd "$dir" && npm install --no-audit --no-fund --silent && npx playwright install chromium)
fi
exec node "$dir/record.mjs" "$@"

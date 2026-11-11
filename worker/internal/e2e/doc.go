// Package e2e holds end-to-end tests for the approve -> effect path (plan
// steps 2.3 and 2.4): local Supabase (Postgres, pg_net trigger, the
// effect-github-comment and mcp Edge Functions) against a fake GitHub API
// that counts comments.
//
// The tests skip unless the harness environment is set; run them with
// scripts/test-effector.sh from the repo root after `supabase start`.
package e2e

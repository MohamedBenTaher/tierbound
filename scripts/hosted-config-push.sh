#!/usr/bin/env bash
# Push supabase/config.toml to the hosted project (plan "Setup smoothing", B2).
# Loads the GitHub OAuth App credentials from ~/.config/tierbound without
# echoing them, applies pending migrations first (the auth hook function must
# exist before Auth is pointed at it), then pushes the config.
set -euo pipefail
cd "$(dirname "$0")/.."
dir="${TIERBOUND_SECRETS_DIR:-$HOME/.config/tierbound}"
for f in github-oauth-client-id github-oauth-client-secret; do
  [[ -s "$dir/$f" ]] || { echo "missing $dir/$f (see runbooks/hosted-setup.md, 'GitHub sign-in')" >&2; exit 1; }
done
TIERBOUND_GITHUB_OAUTH_CLIENT_ID="$(tr -d '[:space:]' <"$dir/github-oauth-client-id")"
TIERBOUND_GITHUB_OAUTH_SECRET="$(tr -d '[:space:]' <"$dir/github-oauth-client-secret")"
export TIERBOUND_GITHUB_OAUTH_CLIENT_ID TIERBOUND_GITHUB_OAUTH_SECRET
supabase db push --linked --yes
# Allowlist the GitHub account gh is signed in as (idempotent). Add more
# approvers with the same insert. Numeric id: logins can be renamed.
gh_id="$(gh api user -q .id)"; gh_login="$(gh api user -q .login)"
[[ "$gh_id" =~ ^[0-9]+$ && "$gh_login" =~ ^[A-Za-z0-9-]+$ ]] || { echo "unexpected gh user" >&2; exit 1; }
supabase db query --remote --yes "insert into public.tierbound_approvers (github_user_id, github_login, note)
  values ($gh_id, '$gh_login', 'owner') on conflict (github_user_id) do update set github_login = excluded.github_login"
supabase config push --yes
echo "GitHub sign-in enabled for $gh_login ($gh_id)."

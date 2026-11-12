-- Approver allowlist + before-user-created hook (plan "Setup smoothing", B2).
--
-- Hosted approvers sign in with GitHub. Public sign-up stays effectively
-- closed: every new auth user passes this hook, which admits only a GitHub
-- identity whose numeric GitHub user id is on the allowlist. The id (not the
-- login) is the key because logins can be renamed and re-registered.
--
-- Owner mapping: Supabase links a new OAuth identity to an existing user with
-- the same verified email, so an approver whose GitHub primary email equals
-- their existing account keeps the same auth.users id and therefore owns the
-- same agents (RLS is owner = auth.uid()). Otherwise a new user is created and
-- the agents must be re-owned once (runbooks/hosted-setup.md).
--
-- The hook is enabled on hosted only ([remotes.hosted.auth.hook...]); the
-- local stack keeps email + password for development.

create table public.tierbound_approvers (
  github_user_id bigint primary key,
  github_login text not null,
  note text,
  added_at timestamptz not null default now()
);

-- Nobody reads or writes this through the Data API; the hook reads it as
-- supabase_auth_admin, operators edit it with SQL.
alter table public.tierbound_approvers enable row level security;
revoke all on public.tierbound_approvers from anon, authenticated;
grant usage on schema public to supabase_auth_admin;
grant select on public.tierbound_approvers to supabase_auth_admin;

create function public.tierbound_before_user_created(event jsonb)
returns jsonb
language plpgsql
stable
set search_path = ''
as $$
declare
  provider text := event -> 'user' -> 'app_metadata' ->> 'provider';
  github_id text := coalesce(
    event -> 'user' -> 'user_metadata' ->> 'provider_id',
    event -> 'user' -> 'user_metadata' ->> 'sub');
begin
  if provider = 'github'
     and github_id ~ '^[0-9]{1,19}$'
     and exists (select 1 from public.tierbound_approvers a where a.github_user_id = github_id::bigint)
  then
    return '{}'::jsonb;
  end if;
  return jsonb_build_object('error', jsonb_build_object(
    'http_code', 403,
    'message', 'tierbound: sign-up is limited to allowlisted GitHub approvers'));
end;
$$;

revoke execute on function public.tierbound_before_user_created(jsonb) from public, anon, authenticated;
grant execute on function public.tierbound_before_user_created(jsonb) to supabase_auth_admin;

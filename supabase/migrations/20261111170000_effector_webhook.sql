-- Plan step 2.3: approvals insert -> effect-github-comment (Database Webhook).
--
-- Mechanism (Supabase docs, "Database Webhooks" > "Call an Edge Function" >
-- "From a migration", checked 2026-11-11): a security definer trigger
-- function reads the URL and the shared secret from Vault and calls the Edge
-- Function with pg_net (async: the HTTP call is made after commit by the
-- pg_net worker, so it never blocks or rolls back the approval).
--
-- Vault entries (set once per environment, outside migrations):
--   tierbound_functions_url            local: http://host.docker.internal:54321
--                                      hosted: https://<ref>.supabase.co
--   tierbound_effector_webhook_secret  must equal the function's
--                                      EFFECTOR_WEBHOOK_SECRET secret
-- supabase/seed.sql sets local-only values. Missing entries fail the approval
-- insert loudly instead of leaving an approved action with no effect.
--
-- The body carries only the action id: the effector re-reads the action and
-- approval as tierbound_effector and trusts nothing else in the request.

create extension if not exists pg_net with schema extensions;

create function public.tierbound_effect_on_approval()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  base_url text;
  secret text;
begin
  select decrypted_secret into base_url
  from vault.decrypted_secrets where name = 'tierbound_functions_url';
  select decrypted_secret into secret
  from vault.decrypted_secrets where name = 'tierbound_effector_webhook_secret';
  if base_url is null or secret is null then
    raise exception 'tierbound: Vault secrets tierbound_functions_url / tierbound_effector_webhook_secret are not set';
  end if;

  perform net.http_post(
    url := base_url || '/functions/v1/effect-github-comment',
    headers := jsonb_build_object(
      'Content-Type', 'application/json',
      'x-tierbound-webhook-secret', secret
    ),
    body := jsonb_build_object(
      'type', tg_op,
      'table', tg_table_name,
      'schema', tg_table_schema,
      'record', jsonb_build_object('action_id', new.action_id)
    ),
    timeout_milliseconds := 30000
  );
  return new;
end;
$$;

revoke execute on function public.tierbound_effect_on_approval() from public, anon, authenticated;

create trigger tierbound_effect_on_approval
after insert on public.approvals
for each row
when (new.decision = 'approved')
execute function public.tierbound_effect_on_approval();

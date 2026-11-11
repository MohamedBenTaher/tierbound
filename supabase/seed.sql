-- Local dev credentials for the tierbound roles (plan step 1.2 connection
-- mapping). The password is local-only and committed on purpose: it is the
-- same category of secret as the local demo JWT secret printed by
-- `supabase status`. A hosted project sets these passwords from Vault instead
-- and never runs this file.

alter role tierbound_worker login password 'tierbound-local-only';
alter role tierbound_effector login password 'tierbound-local-only';

-- Local dev agent (plan step 1.4 soft-skip: evals pre-marked passed for the
-- pinned config until the Weekend 3 eval gate lands). Keep config_hash in sync
-- with `cd worker && go run ./cmd/worker -print-config-hash`.
insert into auth.users (id) values ('00000000-0000-0000-0000-000000000001')
  on conflict do nothing;
insert into public.agents (owner, name, tier, config_hash, evals_passed_hash)
values ('00000000-0000-0000-0000-000000000001', 'triage-drafter', 'draft',
        '53dd453fa89da08d50f68aa6927f352d519b6c637b43d8c42cda0a888d90c939',
        '53dd453fa89da08d50f68aa6927f352d519b6c637b43d8c42cda0a888d90c939')
  on conflict (owner, name) do update
    set config_hash = excluded.config_hash,
        evals_passed_hash = excluded.evals_passed_hash;

-- Effector webhook wiring (plan step 2.3, migration effector_webhook). The
-- database container reaches the host's API gateway via host.docker.internal
-- (Supabase docs, Database Webhooks > Local development). The secret is
-- local-only, like the role passwords above; the function reads the same
-- value from EFFECTOR_WEBHOOK_SECRET.
select vault.create_secret('http://host.docker.internal:54321', 'tierbound_functions_url');
select vault.create_secret('tierbound-local-only', 'tierbound_effector_webhook_secret');

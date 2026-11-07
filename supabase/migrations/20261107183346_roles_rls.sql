-- Plan step 1.2: roles, grants, RLS. Goal (plan): tierbound_worker cannot write
-- approvals/effects; authenticated owner can approve; tierbound_effector can claim effects.
--
-- Connection choice (plan asks to document it): tierbound_worker and
-- tierbound_effector are NOLOGIN privilege containers (SPEC section 5). The
-- worker connects directly to Postgres as a login mapped to tierbound_worker;
-- locally that mapping is enabled in supabase/seed.sql with a local-only
-- password (same category as the local demo JWT secret). On a hosted project
-- the same mapping is created by the operator with a Vault-held password; the
-- grants below stay identical. No code path holds a role with more rights than
-- the container it maps to.

-- create role has no IF NOT EXISTS; guard so `supabase db reset` stays idempotent
-- (roles are cluster-wide and survive a database drop).
do $$
begin
  create role tierbound_worker nologin;
exception when duplicate_object then null;
end $$;

do $$
begin
  create role tierbound_effector nologin;
exception when duplicate_object then null;
end $$;

-- The operator account impersonates the container roles for runbook checks and
-- tests (the plan's `set role tierbound_worker` denial check). INHERIT FALSE
-- keeps impersonation explicit: the roles' own login credentials remain the
-- only path a component gets their rights implicitly.
grant tierbound_worker, tierbound_effector to postgres with set true, inherit false;

grant usage on schema public to tierbound_worker, tierbound_effector, authenticated;

-- worker: propose-only. No grant on approvals or effects (invariant 1, AGENTS.md).
grant insert, select, update on public.runs to tierbound_worker;
grant insert, select on public.proposed_actions to tierbound_worker;
-- agents is read-only for the worker: tier/evals gate check (plan step 1.4).
grant select on public.agents to tierbound_worker;

-- pgmq functions are invoker-rights, so the worker needs the queue schema too.
grant usage on schema pgmq to tierbound_worker;
grant execute on all functions in schema pgmq to tierbound_worker;
grant all on all tables in schema pgmq to tierbound_worker;

-- effector: the only role that writes effects (invariant 3, AGENTS.md).
grant select on public.proposed_actions, public.approvals to tierbound_effector;
grant insert, update, select on public.effects to tierbound_effector;
grant update on public.proposed_actions to tierbound_effector; -- mark executed

alter table public.agents enable row level security;
alter table public.runs enable row level security;
alter table public.proposed_actions enable row level security;
alter table public.approvals enable row level security;
alter table public.effects enable row level security;
alter table public.webhook_deliveries enable row level security;

-- worker policies. Table grants above are the real boundary; these admit the
-- granted tables to the role under RLS and nothing more.
create policy worker_agents on public.agents
  for select to tierbound_worker using (true);
create policy worker_runs on public.runs
  for all to tierbound_worker using (true) with check (true);
create policy worker_proposed_actions on public.proposed_actions
  for all to tierbound_worker using (true) with check (true);

-- effector policies. effects stays effector-only: no authenticated policy exists
-- on it, so PostgREST clients cannot see or touch effects at all (v1 reads
-- effects via SQL/console only).
create policy effector_effects on public.effects
  for all to tierbound_effector using (true) with check (true);
create policy effector_proposed_actions on public.proposed_actions
  for all to tierbound_effector using (true) with check (true);
create policy effector_approvals on public.approvals
  for select to tierbound_effector using (true);

-- owner policies (authenticated): rows are reachable only through the owner
-- chain agents -> runs -> proposed_actions -> approvals (SPEC section 5).
create policy owner_agents on public.agents
  for select to authenticated
  using (owner = (select auth.uid()));

create policy owner_runs on public.runs
  for select to authenticated
  using (exists (
    select 1 from public.agents a
    where a.id = runs.agent_id and a.owner = (select auth.uid())
  ));

create policy owner_proposed_actions_select on public.proposed_actions
  for select to authenticated
  using (exists (
    select 1
    from public.runs r
    join public.agents a on a.id = r.agent_id
    where r.id = proposed_actions.run_id and a.owner = (select auth.uid())
  ));

-- approve/reject flips the action status (plan step 2.2 tool behaviors).
create policy owner_proposed_actions_update on public.proposed_actions
  for update to authenticated
  using (exists (
    select 1
    from public.runs r
    join public.agents a on a.id = r.agent_id
    where r.id = proposed_actions.run_id and a.owner = (select auth.uid())
  ))
  with check (exists (
    select 1
    from public.runs r
    join public.agents a on a.id = r.agent_id
    where r.id = proposed_actions.run_id and a.owner = (select auth.uid())
  ));

create policy owner_approvals on public.approvals
  for all to authenticated
  using (approver = (select auth.uid()))
  with check (
    approver = (select auth.uid())
    and exists (
      select 1
      from public.proposed_actions pa
      join public.runs r on r.id = pa.run_id
      join public.agents a on a.id = r.agent_id
      where pa.id = approvals.action_id and a.owner = (select auth.uid())
    )
  );

-- webhook_deliveries gets no policies on purpose: only the ingest function's
-- service_role client (RLS-bypassing) writes it; everything else is denied.

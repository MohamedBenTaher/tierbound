-- Plan steps 5.4 and 6.1 (docs/V2_ACTIONS.md 4.2): the github_pr_review kind.
--
-- * kind_policy: github_pr_review, min tier draft, executed by the existing
--   effector function.
-- * action_status 'stale': terminal. The effector sets it when the PR head
--   moved after the review was proposed; nothing is written to GitHub.
-- * agent_capabilities: the 'block' capability (owner decision: blocking is
--   earned through the eval gate, never assumed). A blocking review
--   (event REQUEST_CHANGES, or a 'failure' status) may be inserted only by
--   an agent holding 'block' granted for its current config_hash. No grant
--   exists yet, so blocking is built but inactive. Approval stays human for
--   every action either way.
-- * runs.stats: per-run counters (e.g. ungrounded findings dropped).

alter type public.action_status add value if not exists 'stale';

insert into public.kind_policy (kind, min_tier, effector, description) values
  ('github_pr_review', 'draft', 'effect-github-comment', 'one pull request review (+ advisory tierbound/review status)');

alter table public.runs add column stats jsonb;

create table public.agent_capabilities (
  agent_id uuid not null references public.agents (id) on delete cascade,
  capability text not null check (capability in ('block')),
  granted_hash text not null,   -- the config_hash that earned it
  granted_at timestamptz not null default now(),
  primary key (agent_id, capability)
);
alter table public.agent_capabilities enable row level security;
-- No grants to worker, effector or owners: only the database owner (the
-- promote step) writes it, and only the trigger below reads it.

create or replace function public.tierbound_check_action_tier()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_agent_id uuid;
  agent_tier public.agent_tier;
  agent_hash text;
  required public.agent_tier;
begin
  select a.id, a.tier, a.config_hash into v_agent_id, agent_tier, agent_hash
  from public.runs r join public.agents a on a.id = r.agent_id
  where r.id = new.run_id;
  select kp.min_tier into required
  from public.kind_policy kp where kp.kind = new.kind;
  if required is null then
    raise exception 'tierbound: unknown action kind %', new.kind
      using errcode = '23503';
  end if;
  if agent_tier is null or agent_tier < required then
    raise exception 'tierbound: agent tier % may not propose % (requires %)',
      coalesce(agent_tier::text, 'unknown'), new.kind, required
      using errcode = '42501';
  end if;
  if new.kind = 'github_pr_review'
     and (new.payload->>'event' = 'REQUEST_CHANGES' or new.payload #>> '{status,state}' = 'failure')
     and not exists (
       select 1 from public.agent_capabilities c
       where c.agent_id = v_agent_id and c.capability = 'block' and c.granted_hash = agent_hash)
  then
    raise exception 'tierbound: a blocking review needs the block capability for config_hash %', agent_hash
      using errcode = '42501';
  end if;
  return new;
end;
$$;

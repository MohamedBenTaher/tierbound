-- Plan step 4.1 (docs/V2_ACTIONS.md sections 1-2): action kinds become data.
--
-- public.kind_policy lists every kind the system can execute and the minimum
-- agent tier allowed to propose it. proposed_actions.kind references it (the
-- v1 check constraint kind = 'github_comment' goes away), and a before-insert
-- trigger refuses an action whose agent tier is below the kind's min_tier.
-- The trigger is the database half of "a draft agent can never propose a
-- higher-tier kind"; the worker's activation rule is the other half.
--
-- Only kinds with an effector handler are listed (AGENTS.md invariant 5: a
-- kind lands with its vector, handler and MCP renderer together). New tiers
-- (gate, code) are added with the first kind that needs them.

create table public.kind_policy (
  kind text primary key,
  min_tier public.agent_tier not null,
  effector text not null,
  description text not null
);

insert into public.kind_policy (kind, min_tier, effector, description) values
  ('github_comment', 'draft', 'effect-github-comment', 'one comment on an existing issue'),
  ('github_issue',   'draft', 'effect-github-comment', 'one new issue');

alter table public.proposed_actions drop constraint proposed_actions_kind_check;
alter table public.proposed_actions
  add constraint proposed_actions_kind_fkey
  foreign key (kind) references public.kind_policy (kind);

comment on column public.proposed_actions.target is
  'github_comment: {repo, issue_number}; github_issue: {repo}';

alter table public.kind_policy enable row level security;
create policy kind_policy_read on public.kind_policy
  for select to tierbound_worker, tierbound_effector, authenticated using (true);
grant select on public.kind_policy to tierbound_worker, tierbound_effector, authenticated;

-- security definer: the check must see agents and kind_policy no matter which
-- role inserts. It only reads; it writes nothing.
create function public.tierbound_check_action_tier()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  agent_tier public.agent_tier;
  required public.agent_tier;
begin
  select a.tier into agent_tier
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
  return new;
end;
$$;

revoke execute on function public.tierbound_check_action_tier() from public, anon, authenticated;

create trigger tierbound_check_action_tier
  before insert or update of kind, run_id on public.proposed_actions
  for each row execute function public.tierbound_check_action_tier();

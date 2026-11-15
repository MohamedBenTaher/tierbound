-- Plan Phase 5 (V2_ACTIONS 4.2): the review-pr agent's queue and the one
-- capability question its worker may ask.
--
-- jobs_review: messages {repo, pr_number, head_sha, base_sha, delivery_id}.
-- Grants copy jobs_issue (20261114200000): read / set_vt / archive need
-- select, update, delete on q_ and select, insert on a_. Granted here, in
-- the migration that creates the queue, so the role never lags the queue.
select pgmq.create('jobs_review');

grant select, update, delete on pgmq.q_jobs_review to tierbound_worker;
grant select, insert on pgmq.a_jobs_review to tierbound_worker;

-- tierbound_can_block: does this agent hold the block capability for its
-- current config_hash? The worker asks so it can propose REQUEST_CHANGES
-- only when the insert trigger would accept it; the trigger stays the
-- enforcement. security definer because agent_capabilities has no grants
-- (the worker must not read or write capabilities, only ask this).
create or replace function public.tierbound_can_block(p_agent_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1
    from public.agent_capabilities c
    join public.agents a on a.id = c.agent_id
    where c.agent_id = p_agent_id
      and c.capability = 'block'
      and c.granted_hash = a.config_hash
  );
$$;

revoke all on function public.tierbound_can_block(uuid) from public, anon, authenticated;
grant execute on function public.tierbound_can_block(uuid) to tierbound_worker;

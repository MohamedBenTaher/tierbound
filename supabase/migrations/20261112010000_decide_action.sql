-- Atomic approve/reject (roadmap follow-up to plan steps 2.2/2.4).
--
-- Before: mcp inserted the approvals row and then updated
-- proposed_actions.status in two PostgREST calls, and the owner could UPDATE
-- any proposed_actions column (payload, target, status) and INSERT approvals
-- directly. Now public.tierbound_decide_action is the only way an owner
-- decides: one transaction that locks the action row, checks ownership,
-- status = 'proposed', and the hash, inserts the approval, and sets status.
--
-- SECURITY DEFINER, not invoker: the point is to take UPDATE on
-- proposed_actions and INSERT on approvals away from `authenticated`, so the
-- function must run with rights the caller no longer has. It compensates
-- with an empty search_path, an explicit owner check against auth.uid(), and
-- EXECUTE granted to authenticated only.
--
-- The hash is recomputed here, under the row lock, by a SQL twin of
-- hashing.ActionHash / actionHash: sha256 of the canonical JSON of
-- {kind, target, payload} (sorted keys, compact, JSON.stringify escaping --
-- Postgres's JSON string escaping matches it for the v1 value types). It is
-- pinned by the same testdata/action_hash_vectors.json
-- (TestSQLActionHashMatchesGoldenVectors).
--
-- Refusals use PostgREST's PTxxx SQLSTATEs so they surface as HTTP 404/409/403
-- (not 500): not yours or missing, already decided, hash mismatch.

create function public.tierbound_canonical_json(j jsonb)
returns text
language plpgsql
immutable
set search_path = ''
as $$
begin
  case jsonb_typeof(j)
    when 'object' then
      return '{' || coalesce((
        select string_agg(to_json(e.key)::text || ':' || public.tierbound_canonical_json(e.value),
                          ',' order by e.key collate "C")
        from jsonb_each(j) as e), '') || '}';
    when 'array' then
      return '[' || coalesce((
        select string_agg(public.tierbound_canonical_json(a.value), ',' order by a.ord)
        from jsonb_array_elements(j) with ordinality as a(value, ord)), '') || ']';
    else
      return j::text;  -- string (JSON-escaped), integer, true/false, null
  end case;
end;
$$;

create function public.tierbound_action_hash(kind text, target jsonb, payload jsonb)
returns text
language sql
immutable
set search_path = ''
as $$
  select encode(sha256(convert_to(public.tierbound_canonical_json(
    jsonb_build_object('kind', kind, 'target', target, 'payload', payload)), 'UTF8')), 'hex');
$$;

create function public.tierbound_decide_action(
  p_action_id uuid,
  p_decision text,
  p_expected_hash text default null,
  p_reason text default null
) returns jsonb
language plpgsql
security definer
set search_path = ''
as $$
declare
  caller uuid := auth.uid();
  action public.proposed_actions%rowtype;
  action_owner uuid;
  recomputed text;
begin
  if caller is null then
    raise exception 'not signed in' using errcode = '42501';
  end if;
  if p_decision not in ('approved', 'rejected') then
    raise exception 'decision must be approved or rejected' using errcode = '22023';
  end if;

  -- Serializes concurrent decisions on one action (approve vs reject, double
  -- approve): the loser waits here and then sees status <> 'proposed'.
  select * into action from public.proposed_actions where id = p_action_id for update;
  if found then
    select a.owner into action_owner
    from public.runs r join public.agents a on a.id = r.agent_id
    where r.id = action.run_id;
  end if;
  if not found or action_owner is distinct from caller then
    raise exception 'action not found' using errcode = 'PT404';  -- no existence leak
  end if;
  if action.status <> 'proposed' then
    raise exception 'action is %, not proposed', action.status using errcode = 'PT409';
  end if;

  recomputed := public.tierbound_action_hash(action.kind, action.target, action.payload);
  if p_decision = 'approved' then
    if recomputed <> action.payload_hash then
      raise exception 'stored payload_hash does not match the action''s kind/target/payload; data was tampered'
        using errcode = 'PT403';
    end if;
    if p_expected_hash is distinct from recomputed then
      raise exception 'payload_hash does not match the action''s kind/target/payload'
        using errcode = 'PT403';
    end if;
  end if;
  -- A rejection needs no hash from the caller; it records the hash of what
  -- was actually rejected.

  insert into public.approvals (action_id, approver, payload_hash, decision, reason)
  values (p_action_id, caller, recomputed, p_decision, p_reason);
  update public.proposed_actions
  set status = p_decision::public.action_status
  where id = p_action_id;

  return jsonb_build_object('action_id', p_action_id, 'decision', p_decision, 'payload_hash', recomputed);
end;
$$;

revoke execute on function public.tierbound_decide_action(uuid, text, text, text) from public, anon;
grant execute on function public.tierbound_decide_action(uuid, text, text, text) to authenticated;

-- Close the owner-edit hole: owners read their actions and decide through the
-- function above; they no longer write proposed_actions or approvals.
drop policy owner_proposed_actions_update on public.proposed_actions;
drop policy owner_approvals_insert on public.approvals;
revoke insert, update, delete, truncate on public.proposed_actions from anon, authenticated;
revoke insert, update, delete, truncate on public.approvals from anon, authenticated;

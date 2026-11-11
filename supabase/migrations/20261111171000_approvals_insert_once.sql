-- Plan step 2.4: approvals are insert-once. The roles_rls migration gave the
-- owner `for all` on approvals, so a signed-in owner could PATCH the approved
-- payload_hash or DELETE the row through PostgREST after the effect ran
-- (caught by TestOwnerCannotRewriteApproval). An approval is the audit record
-- the effector binds to: the owner may read and insert it, nothing else.

drop policy owner_approvals on public.approvals;

create policy owner_approvals_select on public.approvals
  for select to authenticated
  using (approver = (select auth.uid()));

create policy owner_approvals_insert on public.approvals
  for insert to authenticated
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

-- Belt and braces: no API role may change or remove an approval.
revoke update, delete, truncate on public.approvals from anon, authenticated;

-- Plan step 1.3 support: the ingest entry point for Edge Functions.
-- PostgREST only exposes the public schema, so the ingest function cannot call
-- pgmq.send directly through supabase-js. This wrapper is the single public
-- entry to the jobs queue.
--
-- Dedupe and enqueue run in one transaction (plan step 1.3 idempotency): a
-- replayed X-GitHub-Delivery can never add a second message, and a crash can
-- never leave a delivery recorded without its job enqueued.

create function public.tierbound_ingest_enqueue(
  delivery_id text,
  repo text,
  issue_number integer
) returns boolean
language plpgsql
security definer
set search_path = ''
as $$
begin
  insert into public.webhook_deliveries (delivery_id)
  values (delivery_id)
  on conflict do nothing;

  if not found then
    return false;  -- replayed delivery: already enqueued (or recorded) once
  end if;

  perform pgmq.send('jobs', jsonb_build_object(
    'repo', repo,
    'issue_number', issue_number,
    'delivery_id', delivery_id
  ));
  return true;
end;
$$;

-- Only the ingest function's service_role client may call this.
revoke execute on function public.tierbound_ingest_enqueue(text, text, integer)
  from public, anon, authenticated;
grant execute on function public.tierbound_ingest_enqueue(text, text, integer)
  to service_role;

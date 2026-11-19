-- Plan step 5.2: ingest routes one webhook delivery to the queues of the
-- agents it concerns, in one transaction:
--   issues.opened          -> jobs (triage comment, unchanged) and, for a
--                             human-authored issue, jobs_issue (issue-drafter)
--   pull_request (opened, synchronize, reopened, ready_for_review; same-repo,
--                 not draft, not bot) -> jobs_review (review-pr)
-- The Edge Function decides the routing; this function is the only way in,
-- and it enforces what the function cannot be trusted to: the delivery is
-- recorded once (a redelivered X-GitHub-Delivery enqueues nothing), every
-- queue is on an allowlist, and a job with a dedupe_key is sent at most once
-- per key (review jobs use repo#pr@head_sha, so "opened" then
-- "ready_for_review" on the same head is one review, not two model calls).
--
-- tierbound_ingest_enqueue (plan 1.3) stays for the currently deployed
-- ingest until the new function is deployed; nothing else calls it.

create table public.ingest_job_keys (
  key text primary key,
  delivery_id text not null references public.webhook_deliveries (delivery_id),
  created_at timestamptz not null default now()
);
alter table public.ingest_job_keys enable row level security;
-- No policies and no grants: only the security definer function below writes.

create function public.tierbound_ingest_route(p_delivery_id text, p_jobs jsonb)
returns integer
language plpgsql
security definer
set search_path = ''
as $$
declare
  j jsonb;
  q text;
  n integer := 0;
begin
  if jsonb_typeof(p_jobs) is distinct from 'array' then
    raise exception 'tierbound_ingest_route: jobs must be an array' using errcode = '22023';
  end if;

  insert into public.webhook_deliveries (delivery_id)
  values (p_delivery_id)
  on conflict do nothing;
  if not found then
    return 0;  -- replayed delivery: its jobs were enqueued the first time
  end if;

  for j in select value from jsonb_array_elements(p_jobs) loop
    q := j->>'queue';
    if q is null or q not in ('jobs', 'jobs_issue', 'jobs_review') then
      raise exception 'tierbound_ingest_route: queue % is not routable', q using errcode = '22023';
    end if;
    if jsonb_typeof(j->'message') is distinct from 'object' then
      raise exception 'tierbound_ingest_route: message must be an object' using errcode = '22023';
    end if;
    if j ? 'dedupe_key' then
      insert into public.ingest_job_keys (key, delivery_id)
      values (j->>'dedupe_key', p_delivery_id)
      on conflict do nothing;
      continue when not found;
    end if;
    perform pgmq.send(q, (j->'message') || jsonb_build_object('delivery_id', p_delivery_id));
    n := n + 1;
  end loop;
  return n;
end;
$$;

-- Only the ingest function's service_role client may call this.
revoke execute on function public.tierbound_ingest_route(text, jsonb) from public, anon, authenticated;
grant execute on function public.tierbound_ingest_route(text, jsonb) to service_role;

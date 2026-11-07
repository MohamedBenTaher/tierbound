-- Plan step 1.1: schema, enums, pgmq jobs queue. Data model per docs/SPEC.md section 5.
-- Roles, grants and RLS land in the next migration (plan step 1.2).

create extension if not exists pgmq;
create extension if not exists pgcrypto;

create type public.agent_tier as enum ('read', 'draft');

create type public.action_status as enum (
  'proposed', 'approved', 'rejected', 'executed', 'failed'
);

create type public.effect_status as enum ('claimed', 'posted', 'failed');

create table public.agents (
  id uuid primary key default gen_random_uuid(),
  owner uuid not null references auth.users (id),
  name text not null,
  tier public.agent_tier not null default 'draft',
  config_hash text not null,
  evals_passed_hash text,
  unique (owner, name)
);

create table public.runs (
  id uuid primary key default gen_random_uuid(),
  agent_id uuid not null references public.agents (id),
  job_msg_id bigint,
  attempt int not null default 1,
  status text not null,
  error text,
  started_at timestamptz not null default now(),
  finished_at timestamptz
);

create table public.proposed_actions (
  id uuid primary key default gen_random_uuid(),
  run_id uuid not null references public.runs (id),
  kind text not null check (kind = 'github_comment'),
  target jsonb not null,          -- {repo, issue_number}
  payload jsonb not null,
  payload_hash text not null,
  idempotency_key text not null unique,
  status public.action_status not null default 'proposed',
  created_at timestamptz not null default now()
);

create table public.approvals (
  action_id uuid primary key references public.proposed_actions (id),
  approver uuid not null references auth.users (id),
  payload_hash text not null,
  decision text not null check (decision in ('approved', 'rejected')),
  reason text,
  decided_at timestamptz not null default now()
);

create table public.effects (
  action_id uuid primary key references public.proposed_actions (id),
  status public.effect_status not null default 'claimed',
  external_id text,               -- comment URL once posted
  attempts int not null default 0,
  last_error text,
  claimed_at timestamptz not null default now(),
  executed_at timestamptz
);

select pgmq.create('jobs');

-- Ingest idempotency: one row per GitHub delivery id (X-GitHub-Delivery).
create table public.webhook_deliveries (
  delivery_id text primary key,
  received_at timestamptz not null default now()
);

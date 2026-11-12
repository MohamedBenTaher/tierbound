// tierbound MCP server (plan step 2.2): the human approval surface.
// Five tools over Streamable HTTP, behind Supabase Auth OAuth 2.1. Every tool
// runs as the signed-in user through an RLS-scoped client (owner policies in
// the roles_rls migration), so there is no per-tool authorization code.
//
// Invariant 2 (AGENTS.md): approve_action is refused unless the caller's
// hash, the stored payload_hash, and the {kind, target, payload} hash
// recomputed inside the decide transaction all agree.
import "jsr:@supabase/functions-js/edge-runtime.d.ts";

import { createMcpHandler, McpServer } from "npm:@modelcontextprotocol/server@^2.0.0";
import { pipeline } from "npm:@supabase/middleware@1";
import { withOAuthProtectedResource, withSupabase } from "npm:@supabase/server@1";
import { z } from "npm:zod@^4.3.6";
import type { SupabaseClient } from "npm:@supabase/supabase-js@2";

import { actionHash } from "../_shared/canonical.ts";

type ToolResult = {
  content: { type: "text"; text: string }[];
  isError?: boolean;
};

const uuid = z.string().uuid();

function ok(data: unknown): ToolResult {
  return { content: [{ type: "text", text: JSON.stringify(data) }] };
}

function refuse(error: string): ToolResult {
  return {
    content: [{ type: "text", text: JSON.stringify({ ok: false, error }) }],
    isError: true,
  };
}

// Approve and reject are one database transaction
// (public.tierbound_decide_action): it locks the action, checks ownership,
// status = 'proposed', recomputes the {kind, target, payload} hash in SQL,
// inserts the approvals row, and sets the status. Owners have no direct write
// grants on proposed_actions or approvals, so this is the only path.
async function decide(
  supabase: SupabaseClient,
  args: { action_id: string; decision: "approved" | "rejected"; expected_hash?: string; reason?: string },
): Promise<ToolResult> {
  const { data, error } = await supabase.rpc("tierbound_decide_action", {
    p_action_id: args.action_id,
    p_decision: args.decision,
    p_expected_hash: args.expected_hash ?? null,
    p_reason: args.reason ?? null,
  });
  if (error) return refuse(error.message);
  return ok(data);
}

async function listPendingActions(supabase: SupabaseClient): Promise<ToolResult> {
  const { data, error } = await supabase
    .from("proposed_actions")
    .select("id, kind, target, payload_hash, status, created_at, run_id")
    .eq("status", "proposed")
    .order("created_at", { ascending: true });
  if (error) return refuse(error.message);
  return ok(data);
}

async function getAction(supabase: SupabaseClient, action_id: string): Promise<ToolResult> {
  const { data, error } = await supabase
    .from("proposed_actions")
    .select("id, kind, target, payload, payload_hash, status, run_id, runs(agent_id, agents(name, config_hash))")
    .eq("id", action_id)
    .single();
  if (error) return refuse(error.message);
  return ok({ ...data, recomputed_hash: await actionHash(data) });
}

async function getRun(supabase: SupabaseClient, run_id: string): Promise<ToolResult> {
  const { data, error } = await supabase
    .from("runs")
    .select("id, agent_id, job_msg_id, attempt, status, error, started_at, finished_at")
    .eq("id", run_id)
    .single();
  if (error) return refuse(error.message);
  return ok(data);
}

function buildServer(supabase: SupabaseClient): McpServer {
  const server = new McpServer({ name: "tierbound", version: "0.1.0" });

  server.registerTool(
    "list_pending_actions",
    {
      description: "List proposed actions waiting for your decision",
      inputSchema: z.object({}),
      annotations: { readOnlyHint: true },
    },
    () => listPendingActions(supabase),
  );
  server.registerTool(
    "get_action",
    {
      description: "Show one action's payload, its payload_hash, and the agent config_hash",
      inputSchema: z.object({ action_id: uuid }),
      annotations: { readOnlyHint: true },
    },
    ({ action_id }) => getAction(supabase, action_id),
  );
  server.registerTool(
    "approve_action",
    {
      description: "Approve one action's exact payload by hash. Refused unless the hash matches the canonical payload.",
      inputSchema: z.object({ action_id: uuid, payload_hash: z.string().length(64) }),
    },
    ({ action_id, payload_hash }) => decide(supabase, { action_id, decision: "approved", expected_hash: payload_hash }),
  );
  server.registerTool(
    "reject_action",
    {
      description: "Reject one action with a reason",
      inputSchema: z.object({ action_id: uuid, reason: z.string().min(1).max(500) }),
    },
    ({ action_id, reason }) => decide(supabase, { action_id, decision: "rejected", reason }),
  );
  server.registerTool(
    "get_run",
    {
      description: "Show one run ledger row",
      inputSchema: z.object({ run_id: uuid }),
      annotations: { readOnlyHint: true },
    },
    ({ run_id }) => getRun(supabase, run_id),
  );

  return server;
}

Deno.serve(
  pipeline(
    [
      withOAuthProtectedResource(),
      withSupabase({ auth: "user" }),
    ],
    (req, { supabase }) =>
      createMcpHandler(() => buildServer(supabase)).fetch(req),
  ),
);

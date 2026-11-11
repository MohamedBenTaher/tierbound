// tierbound MCP server (plan step 2.2): the human approval surface.
// Five tools over Streamable HTTP, behind Supabase Auth OAuth 2.1. Every tool
// runs as the signed-in user through an RLS-scoped client (owner policies in
// the roles_rls migration), so there is no per-tool authorization code.
//
// Invariant 2 (AGENTS.md): approve_action recomputes the canonical payload
// hash and refuses unless the caller's hash, the recomputed hash, and the
// stored payload_hash all agree.
import "jsr:@supabase/functions-js/edge-runtime.d.ts";

import { createMcpHandler, McpServer } from "npm:@modelcontextprotocol/server@^2.0.0";
import { pipeline } from "npm:@supabase/middleware@1";
import { withOAuthProtectedResource, withSupabase } from "npm:@supabase/server@1";
import { z } from "npm:zod@^4.3.6";
import type { SupabaseClient } from "npm:@supabase/supabase-js@2";

import { canonicalHash } from "../_shared/canonical.ts";

type ToolResult = {
  content: { type: "text"; text: string }[];
  isError?: boolean;
};

type ProposedAction = {
  id: string;
  payload: unknown;
  payload_hash: string;
  status: string;
};

type LoadResult =
  | { ok: true; action: ProposedAction }
  | { ok: false; error: string };

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

async function loadProposedAction(
  supabase: SupabaseClient,
  action_id: string,
): Promise<LoadResult> {
  const { data, error } = await supabase
    .from("proposed_actions")
    .select("id, payload, payload_hash, status")
    .eq("id", action_id)
    .single();
  if (error) return { ok: false, error: error.message };
  if (data.status !== "proposed") {
    return { ok: false, error: `action is ${data.status}, not proposed` };
  }
  return { ok: true, action: data as ProposedAction };
}

// Both recompute (AGENTS.md invariant 2): the stored column, the payload
// bytes, and the caller's hash must all agree. Returns the recomputed hash
// on success so the approval record binds to exactly that value.
async function verifyApprovalHash(
  action: ProposedAction,
  suppliedHash: string,
): Promise<{ hash: string } | { error: string }> {
  const recomputed = await canonicalHash(action.payload);
  if (recomputed !== action.payload_hash) {
    return { error: "stored payload_hash does not match the canonical payload; data was tampered" };
  }
  if (suppliedHash !== recomputed) {
    return { error: "payload_hash does not match the canonical payload" };
  }
  return { hash: recomputed };
}

async function recordDecision(
  supabase: SupabaseClient,
  decision: { action_id: string; payload_hash: string; verdict: "approved" | "rejected"; reason?: string },
): Promise<string | null> {
  const { data: { user } } = await supabase.auth.getUser();
  const { error: insErr } = await supabase.from("approvals").insert({
    action_id: decision.action_id,
    approver: user!.id,
    payload_hash: decision.payload_hash,
    decision: decision.verdict,
    reason: decision.reason ?? null,
  });
  if (insErr) return insErr.message;
  const { error: updErr } = await supabase
    .from("proposed_actions")
    .update({ status: decision.verdict })
    .eq("id", decision.action_id)
    // The approval insert already fired the effector (async); never overwrite
    // an 'executed' it may have recorded first.
    .eq("status", "proposed");
  return updErr ? updErr.message : null;
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
  return ok({ ...data, recomputed_hash: await canonicalHash(data.payload) });
}

async function approveAction(
  supabase: SupabaseClient,
  action_id: string,
  payload_hash: string,
): Promise<ToolResult> {
  const loaded = await loadProposedAction(supabase, action_id);
  if (!loaded.ok) return refuse(loaded.error);
  const verified = await verifyApprovalHash(loaded.action, payload_hash);
  if ("error" in verified) return refuse(verified.error);
  const recErr = await recordDecision(supabase, {
    action_id,
    payload_hash: verified.hash,
    verdict: "approved",
  });
  if (recErr) return refuse(recErr);
  return ok({ approved: true, action_id, payload_hash: verified.hash });
}

async function rejectAction(
  supabase: SupabaseClient,
  action_id: string,
  reason: string,
): Promise<ToolResult> {
  const loaded = await loadProposedAction(supabase, action_id);
  if (!loaded.ok) return refuse(loaded.error);
  const recErr = await recordDecision(supabase, {
    action_id,
    payload_hash: "",
    verdict: "rejected",
    reason,
  });
  if (recErr) return refuse(recErr);
  return ok({ rejected: true, action_id });
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
    ({ action_id, payload_hash }) => approveAction(supabase, action_id, payload_hash),
  );
  server.registerTool(
    "reject_action",
    {
      description: "Reject one action with a reason",
      inputSchema: z.object({ action_id: uuid, reason: z.string().min(1).max(500) }),
    },
    ({ action_id, reason }) => rejectAction(supabase, action_id, reason),
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

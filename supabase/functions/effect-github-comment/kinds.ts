// Per-kind handlers for the effector (plan step 4.3, docs/V2_ACTIONS.md 4.1).
// index.ts owns the protocol (verify the hash, claim, takeover, record); a
// handler only knows how to validate its {target, payload}, find an earlier
// post by marker (timeout-after-success), and post once. Every kind in
// public.kind_policy whose effector is this function has a handler here.
import type { GitHubApp } from "./github.ts";

export type StoredAction = { kind: string; target: unknown; payload: unknown };

export type Handler = {
  // Read-only checks against GitHub before posting; a string refuses.
  precheck(gh: GitHubApp): Promise<string | null>;
  // html_url of an earlier post carrying marker, made after `since`.
  findExisting(gh: GitHubApp, marker: string, since: string): Promise<string | null>;
  post(gh: GitHubApp, marker: string): Promise<string>;
};

const MAX_TITLE = 256;
const MAX_LABELS = 10;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function nonEmpty(v: unknown): v is string {
  return typeof v === "string" && v.length > 0;
}

function commentHandler(target: Record<string, unknown>, payload: Record<string, unknown>): Handler | string {
  if (!nonEmpty(payload.body)) return "payload.body missing";
  const t = { repo: String(target.repo), issue_number: Number(target.issue_number) };
  const body = payload.body;
  return {
    precheck: () => Promise.resolve(null),
    findExisting: (gh: GitHubApp, marker: string) => gh.findMarkedComment(t, marker),
    post: (gh: GitHubApp, marker: string) => gh.postComment(t, `${body}\n\n${marker}`),
  };
}

function issueLabels(v: unknown): string[] | string {
  if (!Array.isArray(v) || v.length > MAX_LABELS || !v.every(nonEmpty)) {
    return `payload.labels must be at most ${MAX_LABELS} non-empty strings`;
  }
  return v as string[];
}

type IssueFields = { title: string; body: string; labels: string[] };

function validTitle(v: unknown): v is string {
  return nonEmpty(v) && v.length <= MAX_TITLE;
}

function issueFields(payload: Record<string, unknown>): IssueFields | string {
  const { title, body } = payload;
  if (!validTitle(title)) return `payload.title must be 1-${MAX_TITLE} characters`;
  if (!nonEmpty(body)) return "payload.body missing";
  const labels = issueLabels(payload.labels);
  return typeof labels === "string" ? labels : { title, body, labels };
}

function issueHandler(target: Record<string, unknown>, payload: Record<string, unknown>): Handler | string {
  const fields = issueFields(payload);
  if (typeof fields === "string") return fields;
  const { title, body, labels } = fields;
  const repo = String(target.repo);
  return {
    // V2_ACTIONS 4.1: the effector never creates labels; an unknown one refuses.
    precheck: async (gh: GitHubApp) => {
      const known = new Set(await gh.listLabels(repo));
      const missing = labels.filter((l) => !known.has(l));
      return missing.length ? `unknown_label: ${missing.join(", ")}` : null;
    },
    findExisting: (gh: GitHubApp, marker: string, since: string) => gh.findMarkedIssue(repo, marker, since),
    post: (gh: GitHubApp, marker: string) => gh.createIssue(repo, { title, body: `${body}\n\n${marker}`, labels }),
  };
}

const HANDLERS: Record<string, typeof issueHandler> = {
  github_comment: commentHandler,
  github_issue: issueHandler,
};

// The handler for one stored action, or the reason it cannot be executed.
export function handlerFor(action: StoredAction): Handler | string {
  const make = HANDLERS[action.kind];
  if (!make) return `unsupported kind ${action.kind}`;
  if (!isRecord(action.target) || !isRecord(action.payload)) return "target and payload must be objects";
  return make(action.target, action.payload);
}

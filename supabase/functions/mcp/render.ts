// Per-kind previews for the approval surface (plan step 4.4,
// docs/V2_ACTIONS.md section 6). The preview is what a human reads before
// approving; the hash still binds the raw {kind, target, payload}, so a
// preview bug can never change what executes. Unknown kinds fall back to the
// raw JSON, never to an empty string.

import { renderReview } from "./render_review.ts";

type Row = { kind: string; target: unknown; payload?: unknown };
type Obj = Record<string, unknown>;

function obj(v: unknown): Obj {
  return typeof v === "object" && v !== null ? v as Obj : {};
}

function labelsOf(payload: Obj): string {
  return Array.isArray(payload.labels) && payload.labels.length ? payload.labels.join(", ") : "(none)";
}

const RENDERERS: Record<string, (target: Obj, payload: Obj) => string> = {
  github_comment: (t, p) => `Comment on ${t.repo}#${t.issue_number}\n\n${p.body}`,
  github_issue: (t, p) => `New issue in ${t.repo}\nTitle: ${p.title}\nLabels: ${labelsOf(p)}\n\n${p.body}`,
  github_pr_review: renderReview,
};

const SUMMARIES: Record<string, (target: Obj) => string> = {
  github_comment: (t) => `comment on ${t.repo}#${t.issue_number}`,
  github_issue: (t) => `new issue in ${t.repo}`,
  github_pr_review: (t) => `review of ${t.repo}#${t.pr_number} at ${String(t.head_sha).slice(0, 7)}`,
};

export function preview(row: Row): string {
  const render = RENDERERS[row.kind];
  if (!render) return JSON.stringify({ kind: row.kind, target: row.target, payload: row.payload }, null, 2);
  return render(obj(row.target), obj(row.payload));
}

export function summary(row: Row): string {
  const sum = SUMMARIES[row.kind];
  return sum ? sum(obj(row.target)) : `${row.kind} ${JSON.stringify(row.target)}`;
}

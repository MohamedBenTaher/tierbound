// github_pr_review handler (plan step 5.4, docs/V2_ACTIONS.md 4.2).
//
// Target {repo, pr_number, head_sha}; payload {event, body, comments,
// status, findings}. findings is hashed (the approver saw it) but never
// written; body, comments and status are.
//
//   precheck  - the PR head must still be head_sha, else the action is
//               stale and nothing is written (a review of other code).
//   post      - one POST /reviews pinned to commit_id = head_sha with the
//               marker, then the advisory status (if any).
//   takeover  - a review carrying the marker means the review landed; only
//               the status is (re)posted. Statuses are idempotent: the
//               latest state per context wins.
import type { GitHubApp, ReviewComment, ReviewStatus } from "./github.ts";
import type { Handler } from "./kinds.ts";

type Obj = Record<string, unknown>;

const SHA_RE = /^[0-9a-f]{40}$/;
const EVENTS = new Set(["COMMENT", "REQUEST_CHANGES"]);
const STATES = new Set(["success", "failure"]);
const MAX_COMMENTS = 50;
const MAX_STATUS_DESCRIPTION = 140;

type ReviewTarget = { repo: string; pr_number: number; head_sha: string };
type Review = { target: ReviewTarget; event: string; body: string; comments: ReviewComment[]; status: ReviewStatus | null };

// [ok, message] pairs; the first failing message wins.
type Check = [boolean, string];

function firstFailure(checks: Check[]): string | null {
  const bad = checks.find(([ok]) => !ok);
  return bad ? bad[1] : null;
}

function nonEmpty(v: unknown): v is string {
  return typeof v === "string" && v.length > 0;
}

function positiveInt(v: unknown): v is number {
  return Number.isInteger(v) && (v as number) > 0;
}

function isSha(v: unknown): boolean {
  return typeof v === "string" && SHA_RE.test(v);
}

function isSide(v: unknown): boolean {
  return v === "RIGHT" || v === "LEFT";
}

function obj(v: unknown): Obj {
  return (v ?? {}) as Obj;
}

function reviewTarget(t: Obj): ReviewTarget | string {
  const err = firstFailure([
    [nonEmpty(t.repo), "target.repo missing"],
    [positiveInt(t.pr_number), "target.pr_number must be a positive integer"],
    [isSha(t.head_sha), "target.head_sha must be a 40-hex commit sha"],
  ]);
  return err ?? { repo: t.repo as string, pr_number: t.pr_number as number, head_sha: t.head_sha as string };
}

function validComment(c: unknown): c is ReviewComment {
  const o = obj(c);
  return [nonEmpty(o.path), positiveInt(o.line), isSide(o.side), nonEmpty(o.body)].every(Boolean);
}

function descriptionOf(o: Obj): string {
  return typeof o.description === "string" ? o.description : "";
}

function reviewStatus(s: unknown): ReviewStatus | null | string {
  if (s == null) return null;
  const o = obj(s);
  const err = firstFailure([
    [STATES.has(String(o.state)), "status.state must be success or failure"],
    [o.context === "tierbound/review", "status.context must be tierbound/review"],
    [descriptionOf(o).length <= MAX_STATUS_DESCRIPTION, "status.description too long"],
  ]);
  return err ?? { state: o.state as "success" | "failure", context: "tierbound/review", description: descriptionOf(o) };
}

function reviewComments(v: unknown): ReviewComment[] | string {
  const ok = Array.isArray(v) && v.length <= MAX_COMMENTS && v.every(validComment);
  if (!ok) return `payload.comments must be at most ${MAX_COMMENTS} {path, line, side, body}`;
  return (v as ReviewComment[]).map((c) => ({ path: c.path, line: c.line, side: c.side, body: c.body }));
}

function parseReview(target: Obj, payload: Obj): Review | string {
  const t = reviewTarget(target);
  const comments = reviewComments(payload.comments);
  const status = reviewStatus(payload.status);
  const err = firstFailure([
    [typeof t !== "string", String(t)],
    [EVENTS.has(String(payload.event)), "payload.event must be COMMENT or REQUEST_CHANGES"],
    [nonEmpty(payload.body), "payload.body missing"],
    [typeof comments !== "string", String(comments)],
    [typeof status !== "string", String(status)],
  ]);
  if (err) return err;
  return {
    target: t as ReviewTarget,
    event: String(payload.event),
    body: payload.body as string,
    comments: comments as ReviewComment[],
    status: status as ReviewStatus | null,
  };
}

async function postStatus(gh: GitHubApp, r: Review): Promise<void> {
  if (r.status) await gh.createStatus(r.target.repo, r.target.head_sha, r.status);
}

export function reviewHandler(target: Obj, payload: Obj): Handler | string {
  const r = parseReview(target, payload);
  if (typeof r === "string") return r;
  const { repo, pr_number, head_sha } = r.target;
  return {
    precheck: async (gh: GitHubApp) => {
      const head = await gh.pullHeadSha(repo, pr_number);
      return head === head_sha
        ? null
        : { stale: true, error: `stale_head: PR head is ${head}, the review was approved for ${head_sha}` };
    },
    findExisting: async (gh: GitHubApp, marker: string) => {
      const url = await gh.findMarkedReview(repo, pr_number, marker);
      if (url) await postStatus(gh, r);
      return url;
    },
    post: async (gh: GitHubApp, marker: string) => {
      const url = await gh.createReview(repo, pr_number, {
        commit_id: head_sha,
        event: r.event,
        body: `${r.body}\n\n${marker}`,
        comments: r.comments,
      });
      await postStatus(gh, r);
      return url;
    },
  };
}

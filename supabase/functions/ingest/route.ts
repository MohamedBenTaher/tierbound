// Plan step 5.2: which agent queues one GitHub delivery feeds. Pure: no I/O,
// so the routing table is readable in one place. The database function
// tierbound_ingest_route enforces delivery dedupe, the queue allowlist and
// per-head dedupe; this module only decides.
//
//   issues.opened               -> jobs (triage comment, as in v1, every issue)
//                                  + jobs_issue (issue-drafter) unless the
//                                  author is a bot or the issue is tierbound's
//                                  own (it carries the tierbound marker)
//   pull_request opened / synchronize / reopened / ready_for_review
//                               -> jobs_review (review-pr) unless draft,
//                                  bot-authored, from a fork, or not open
//   ping, anything else         -> nothing

type Obj = Record<string, unknown>;

export type Job = { queue: string; message: Obj; dedupe_key?: string };
export type Routed = { jobs: Job[] } | { ignored: string } | { error: string };

const REVIEW_ACTIONS = ["opened", "synchronize", "reopened", "ready_for_review"];
const MARKER = "<!-- tierbound:";
const SHA = /^[0-9a-f]{40}$/;

function obj(v: unknown): Obj {
  return typeof v === "object" && v !== null ? v as Obj : {};
}

function isBot(user: unknown): boolean {
  const u = obj(user);
  return u.type === "Bot" || String(u.login ?? "").endsWith("[bot]");
}

function positiveInt(v: unknown): v is number {
  return typeof v === "number" && Number.isInteger(v) && v > 0;
}

function repoOf(body: Obj): string {
  const name = obj(body.repository).full_name;
  return typeof name === "string" ? name : "";
}

// First reason in a flat list of [failed, reason] pairs, or null.
function firstReason(checks: [boolean, string][]): string | null {
  return checks.find(([failed]) => failed)?.[1] ?? null;
}

function malformed(checks: [boolean, string][]): boolean {
  return firstReason(checks) !== null;
}

// The issue-drafter drafts only from humans' issues, never from its own
// (tierbound-created issues carry the marker), so it cannot feed itself.
function drafts(issue: Obj): boolean {
  return !isBot(issue.user) && !String(issue.body ?? "").includes(MARKER);
}

function routeIssue(body: Obj): Routed {
  if (body.action !== "opened") {
    return { ignored: "issues action is not opened" };
  }
  const issue = obj(body.issue);
  const repo = repoOf(body);
  if (malformed([[repo === "", "repo"], [!positiveInt(issue.number), "number"]])) {
    return { error: "malformed issues.opened payload" };
  }
  const message = { repo, issue_number: issue.number };
  const triage: Job = { queue: "jobs", message };
  return { jobs: drafts(issue) ? [triage, { queue: "jobs_issue", message }] : [triage] };
}

function prSkipReason(body: Obj, pr: Obj, repo: string): string | null {
  const head = obj(pr.head);
  return firstReason([
    [!REVIEW_ACTIONS.includes(String(body.action)), "pull_request action is not reviewed"],
    [pr.state !== "open", "pull request is not open"],
    [pr.draft === true, "draft pull request"],
    [isBot(pr.user), "bot-authored pull request"],
    // Same-repo PRs only (V2_ACTIONS 9): a fork's head is untrusted code
    // and its author cannot be held to the repo's rules.
    [obj(head.repo).full_name !== repo, "pull request from a fork"],
  ]);
}

function shaOf(ref: unknown): string {
  return String(obj(ref).sha ?? "");
}

function routePullRequest(body: Obj): Routed {
  const pr = obj(body.pull_request);
  const repo = repoOf(body);
  const skip = prSkipReason(body, pr, repo);
  if (skip !== null) {
    return { ignored: skip };
  }
  const message = { repo, pr_number: pr.number, head_sha: shaOf(pr.head), base_sha: shaOf(pr.base) };
  if (
    malformed([
      [repo === "", "repo"],
      [!positiveInt(message.pr_number), "number"],
      [!SHA.test(message.head_sha), "head sha"],
      [!SHA.test(message.base_sha), "base sha"],
    ])
  ) {
    return { error: "malformed pull_request payload" };
  }
  const dedupe_key = `review:${repo}#${message.pr_number}@${message.head_sha}`;
  return { jobs: [{ queue: "jobs_review", message, dedupe_key }] };
}

const ROUTES: Record<string, (body: Obj) => Routed> = {
  issues: routeIssue,
  pull_request: routePullRequest,
};

export function route(event: string | null, body: unknown): Routed {
  const handler = ROUTES[event ?? ""];
  return handler ? handler(obj(body)) : { ignored: `event ${event} is not routed` };
}

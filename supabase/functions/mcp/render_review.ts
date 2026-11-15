// Preview of a github_pr_review action (plan step 5.4, V2_ACTIONS 4.2 "MCP"):
// the verdict line, a findings table with the quoted rule text, each inline
// comment exactly as it will be posted, and the pinned head sha with a link.

type Obj = Record<string, unknown>;

function obj(v: unknown): Obj {
  return typeof v === "object" && v !== null ? v as Obj : {};
}

function list(v: unknown): Obj[] {
  return Array.isArray(v) ? v.map(obj) : [];
}

function blockingRule(p: Obj): string {
  const blocker = list(p.findings).find((f) => f.severity === "blocker");
  const rule = obj(blocker?.rule);
  return p.event === "REQUEST_CHANGES" && blocker ? ` · blocking rule: ${rule.source}#${rule.anchor}` : "";
}

function verdictLine(p: Obj): string {
  const status = obj(p.status);
  const statusText = p.status ? `status ${status.state} (advisory)` : "no status";
  return `${p.event} · ${statusText}${blockingRule(p)}`;
}

function findingRow(f: Obj): string {
  const rule = obj(f.rule);
  const evidence = list(f.evidence).length ? ` · evidence ${(f.evidence as string[]).join(", ")}` : "";
  return `- ${f.path}:${f.line} [${f.severity}] ${rule.source}#${rule.anchor}: "${rule.quote}"${evidence}\n  ${f.message}`;
}

function commentBlock(c: Obj): string {
  return `--- ${c.path}:${c.line} (${c.side})\n${c.body}`;
}

export function renderReview(t: Obj, p: Obj): string {
  const head = String(t.head_sha);
  const findings = list(p.findings);
  const comments = list(p.comments);
  return [
    `Review of ${t.repo}#${t.pr_number} at ${head.slice(0, 7)} (https://github.com/${t.repo}/pull/${t.pr_number}/files/${head})`,
    verdictLine(p),
    "",
    `Findings (${findings.length}):`,
    ...findings.map(findingRow),
    "",
    `Review body:\n${p.body}`,
    "",
    `Inline comments (${comments.length}):`,
    ...comments.map(commentBlock),
  ].join("\n");
}

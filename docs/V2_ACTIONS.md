# tierbound v2 — more action kinds (design)

> Status: **design** (2026-11-14), revised the same day with the owner's decisions (§10). Phase 4 (`github_issue`) is being implemented; everything else is design only. It extends [SPEC.md](SPEC.md); the build steps are in [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) Phases 4–8.

v1 does one thing: an agent drafts a GitHub comment, a human approves its exact hash over MCP, and an effector posts it exactly once. v2 adds four kinds of work. None of them weakens the core invariant:

> **The agent proposes. A human approves the exact hashed bytes over MCP. A credential-holding effector executes exactly once. The worker never holds a GitHub write credential.**

| Agent (job) | Action kind it proposes | What the effect writes | Tier |
| --- | --- | --- | --- |
| any (first: `review-repo`, follow-ups) | `github_issue` | one new issue | `draft` |
| `review-pr` | `github_pr_review` | one PR review (+ optional `tierbound/review` status) | `draft`; blocking needs `gate` |
| `review-repo` | `github_report` | create or update one tracking issue | `draft` |
| `review-repo` | `github_security_advisory` | one **private** draft repository security advisory | `draft` |
| `pr-fix` | `github_pr_fix` | one branch `tierbound/fix-<action_id>` + one **draft** PR | `code` |

---

## 1. What stays the same

- **One hash.** Every kind is hashed with the same canonical envelope `sha256({kind, target, payload})` (keys sorted, compact, `JSON.stringify` escaping). Go, TS and SQL keep sharing `testdata/action_hash_vectors.json`, with new vectors added per kind. A payload carries **everything the effect writes**: if a byte reaches GitHub, it is in the hash.
- **One decision path.** `tierbound_decide_action` stays the only write path for approvers. It still locks the action, checks owner, status and recomputed hash, and records the approval in one transaction.
- **One claim protocol.** `effects` keeps the unique claim on `action_id`, lease takeover and `attempts` as a fencing token (AGENTS.md invariant 3, TLA+-checked). Multi-step effects add a `progress` column (below). They never change the claim rule.
- **Activation rule.** No proposal unless `running config hash = agents.config_hash = evals_passed_hash` (step 3.3). v2 adds per-capability grants on top. It never relaxes this rule.
- **No dashboard.** MCP stays the UI. Approvers read diffs and reviews through `get_action` and MCP resources (§6).

## 2. Tiers and capabilities

Tiers are ordered by blast radius. An agent's tier caps which kinds it may propose, and the **database** enforces it rather than the worker:

| Tier | May propose | Why this tier |
| --- | --- | --- |
| `read` | nothing (reads only) | v1 |
| `draft` | `github_comment`, `github_issue`, `github_report`, non-blocking `github_pr_review` | public text, reversible (edit/delete), no effect on anyone's merge |
| `gate` | + blocking `github_pr_review` (`REQUEST_CHANGES` and/or `tierbound/review = failure`) | changes whether someone else can merge |
| `code` | + `github_pr_fix` | writes git objects and a branch into the repo |

- **Kind policy table.** `kind_policy(kind, min_tier, effector, capability)` maps each kind to its minimum tier and the Edge Function that executes it.
- **Insert check.** A `before insert` trigger on `proposed_actions` (security definer) joins `runs → agents` and refuses a kind above the agent's tier. A worker bug or a prompt injection can't propose a PR fix from a review agent.
- **Capabilities are earned per config hash.** Today's single `evals_passed_hash` becomes the `propose` capability. A new table `agent_capabilities(agent_id, config_hash, capability, metrics jsonb, granted_at)` holds the rest, and only `evals/promote.sh` (service role) writes it after the eval gate passes the capability's threshold. The first extra capability is `block`, for `review-pr`.
- **Blocking check.** The same insert trigger refuses a `github_pr_review` with `event = REQUEST_CHANGES` or `status.state = failure` unless `block` is granted for the run's `config_hash`. This needs a new column, `runs.config_hash`.
- **Approval is always human, for every kind, permanently** (owner decision, §10). There is no auto-approve tier, no policy approver and no `auto_comment`. A capability only decides what an agent may *propose*; it never replaces the human decision.

## 3. One GitHub App, and what carries least privilege instead

**Decision (owner, 2026-11-14): one App.** The existing `tierbound-mbt` App is extended instead of registering a reader App and a fixer App. It is installed on `tierbound` only (today the installation covers all repos; narrowing it is an owner click). Its permissions grow as phases ship:

| Permission | Needed by | Phase |
| --- | --- | --- |
| Issues: write | comments, `github_issue`, `github_report` | v1, 4 |
| Metadata: read | everything | v1 |
| Pull requests: write | reviews (`github_pr_review`), draft PRs (`github_pr_fix`) | 5, 8 |
| Commit statuses: write | advisory `tierbound/review` status | 5 |
| Actions: read | evidence artifacts | 5 |
| Repository security advisories: write | `github_security_advisory` | 7 |
| Contents: write | branches and commits for `github_pr_fix` | 8 |

**The honest trade-off.** Three Apps would have meant the worker's key could only read, and only the fixer function's key could write code. With one App, **one key can do everything above**: comment, review, open PRs, write branches and draft advisories on `tierbound`. If that key leaks, the damage is bounded by the installation (one repo) and by branch protection on `main`, nothing finer. Note that Supabase Edge Function secrets are **project-wide** environment variables, so even with three Apps every function in the project could have read every key; per-function isolation was never available from secret scoping alone. What one App gives up is mostly the *worker* side, and that is restored by a token broker (below).

**What least privilege rests on now:**
1. **The worker never holds the App key.** It gets short-lived, **down-scoped** installation tokens from a small Edge Function, `github-read-token`. The function mints `POST /app/installations/{id}/access_tokens` with `repositories: [<repo>]` and `permissions: {contents: read, issues: read, pull_requests: read, actions: read, metadata: read}`, and GitHub refuses to issue more than was requested. The worker authenticates with its own shared secret (not the effector's). So the worker's maximum power is still "read one repo for an hour". This replaces `GITHUB_READ_TOKEN`.
2. **Only effectors write**, and only through the claim protocol after a verified approval. No other function imports the GitHub write client.
3. **The database tier trigger** (§2) stops an agent proposing a kind above its tier, so a write the effectors would accept can only come from an agent allowed to propose it, and then only after a human approves the exact hash.
4. **The code path is separate.** `github_pr_fix` executes in its own Edge Function, `effect-github-pr`. It contains the only calls to the git data API, the hard deny-list (§4.3) and the `tierbound/fix-*` branch rule. The speaker effector `effect-github` has no code-writing calls at all.
5. **Branch protection on `main`** (owner click before Phase 8): require a pull request, no bypass for the App, no force-push or deletion. Even the full App key cannot change `main` without a reviewed merge, and the effector never calls merge.

## 4. The four kinds

### 4.1 `github_issue` — create an issue

- **Tier:** `draft`.
- **Target:** `{repo}`.
- **Payload:** `{title, body, labels[]}`. No assignees and no milestone: assigning people is a must-not.
- **Hash:** the full envelope, so title, body (with the marker appended by the effector at a fixed position, **not** in the hash input) and the sorted label list are bound.
- **Effector (`effect-github`):**
  1. Verify the approval hash, then claim.
  2. Refuse labels that don't exist in the repo at effect time, with the clean error `unknown_label`. (**verify** whether the issues API silently creates unknown labels; either way, refusing keeps the effect exactly what was approved.)
  3. `POST /issues` with `body + "\n\n<!-- tierbound:<action_id> -->"`.
  4. Record `external_id = html_url`.
- **Idempotency:** on takeover, list issues with `creator=<app>[bot]&since=<claimed_at>&state=all` and match the marker. A hit is recorded, not re-posted. Search is avoided because the search index lags.
- **Failure modes:**
  - 4xx validation (`unknown_label`, title too long) → `failed` with `last_error`, no retry.
  - 5xx or timeout → lease takeover with the marker check (postmortem-001 path).
- **Eval:** reuses the triage rubric style: must-not rules (no promises, dates, @-mentions, assignment) plus structure (title ≤ 80 chars, body has *context*, *evidence*, *suggested next step*).
- **Gate threshold:** mean ≥ 0.75, zero must-not hits. **Golden set:** ~12 cases drawn from `github_report` items and postmortem action items.
- **MCP:** `get_action` renders the title, labels and body as the exact markdown, with a link to the repo's label list.
- **Demo:** ~5 s, "the report agent opens one issue for a dead export" (comes for free with review-repo).

### 4.2 `github_pr_review` — review a PR (agent `review-pr`)

**Trigger:** `pull_request` `opened` / `synchronize` / `ready_for_review`, same-repo PRs only in v2 (fork PRs: §9). `ingest` enqueues `{repo, pr, head_sha, base_sha}` once the evidence workflow for `head_sha` has finished. That means a `workflow_run` `completed` webhook for `tierbound-evidence`, plus the PR event, joined in `ingest` by `head_sha`.

**Inputs the worker reads** (down-scoped read token from `github-read-token`, §3):
- the PR diff (`GET /pulls/{n}/files`, patch per file);
- the repo's rules **at `base_sha`**: `AGENTS.md`, plus the rule files listed in the agent config, such as `.cursor/skills/**/SKILL.md` readability and quality guides and `docs/review-rules.md`;
- the deterministic evidence artifact from §7: fallow, golangci-lint and gosec findings, restricted to changed lines.

Rules are read at the base commit so that a PR can't weaken the rules it's judged by.

**What the LLM produces — findings, not verdicts:**
```json
{ "path": "worker/internal/propose/propose.go", "line": 142, "side": "RIGHT",
  "severity": "blocker|major|minor|nit",
  "rule": {"source": "AGENTS.md", "anchor": "invariants-1", "quote": "The worker never holds write credentials."},
  "evidence": ["fallow:unused-export:3f2a…"],
  "message": "…", "suggestion": "…" }
```

**Grounding is checked by code, before any proposal.** A finding is dropped (and counted as `ungrounded` in the run ledger) unless all three hold:
- `path:line` falls inside a hunk of the diff (added or context line on the RIGHT side);
- `rule.quote` is a verbatim substring of the cited rule file at `base_sha`;
- every `evidence` id exists in the evidence artifact.

**The verdict is policy, not the model's choice.** `review-policy` sits in the agent config and is part of `config_hash`. It lists which rule anchors can block, for example AGENTS.md invariants and fallow `unused-file` in changed code. The verdict is `REQUEST_CHANGES` and `tierbound/review = failure` only if some **grounded** finding has severity `blocker` **and** cites a blocking rule **and** the config holds the `block` capability. Otherwise the event is `COMMENT` and the status is `success` (or no status, if the status is disabled).

**Target and payload:**
- **Target:** `{repo, pr_number, head_sha}`.
- **Payload:** `{event: "COMMENT"|"REQUEST_CHANGES", body, comments: [{path, line, side, body}], status: {state, context: "tierbound/review", description} | null, findings: [...]}`.
- **Hash:** the full envelope. `findings` is in the payload so the approver's view and the hash match, even though only `body`, `comments` and `status` are written.

**Effector (`effect-github`):**
1. Verify the hash, then claim.
2. **Freshness:** `GET /pulls/{n}` must still have `head.sha == head_sha`. Otherwise mark the action **`stale`** (new terminal status), write nothing, and the next `synchronize` produces a fresh review.
3. `POST /pulls/{n}/reviews` with `commit_id = head_sha`, `event`, `body` + marker, and `comments`.
4. Save `progress.review_id`.
5. If `status` is set, `POST /statuses/{head_sha}`. Statuses are idempotent, because the latest state per context wins.
6. Record `external_id = review html_url`.

**Idempotency:** on takeover, list the PR's reviews and match the marker in the body. If found, skip step 3 and continue at step 5.

**Failure modes:**
- head moved → `stale`;
- a comment line no longer in the diff (422) → `failed`, `invalid_comment_position`, no partial review, because the review is one API call;
- review posted but status failed → `progress` lets the retry post only the status.

**Advisory, not required (owner decision, §10).** `tierbound/review` is **not** a required check on `main`. A blocking verdict (earned through the `block` capability and approved by a human) posts `REQUEST_CHANGES` and a `failure` status, which shows red on the PR but does not stop a merge. Making it required later is one branch-protection click and needs no code change.

**Eval (golden PRs):**
- **Fixtures:** ~20 recorded PRs as `{diff, rules@base, evidence, expected_findings[{path, line±3, rule anchor, severity}], expected_verdict}`: ~12 with seeded violations and ~8 clean PRs as the false-positive control. Examples of seeded violations:
  - a migration that grants `INSERT` on `approvals` to the worker role;
  - `--no-verify` in a script;
  - an exported function that fallow reports unused;
  - a 300-line function that breaks a readability rule.
- **Metrics:**
  - **precision**: findings matching an expected one by path, line ±3 and rule;
  - **recall**;
  - **grounding rate** before the validator, which shows how much the model invents;
  - **false-block rate**: blocking verdicts on clean PRs.
- **Gates:**
  - **`propose` capability:** precision ≥ 0.70, recall ≥ 0.50, grounding ≥ 0.90.
  - **`block` capability** (stricter, separate threshold): precision on blockers ≥ 0.90, **zero** false blocks on the clean set, recall on blockers ≥ 0.60.
- **Replay:** recorded outputs per `config_hash`, deterministic in CI, the same mechanism as step 3.2.

**MCP:** `get_action` renders the verdict line (`REQUEST_CHANGES · status failure · blocking rule: AGENTS.md#invariants-1`). Then a findings table: file:line, severity, the **quoted** rule text and the evidence ids. Then each inline comment exactly as it will be posted, and the head SHA with a link to the PR diff.

**Model:** provider-pluggable (§11); Claude or GPT through an API key.

**Demo:** ~10 s. A PR that adds `grant insert on approvals to tierbound_worker`. The review cites AGENTS.md invariant 1 at the exact line, Mohamed approves, and the PR shows `REQUEST_CHANGES` and a red (advisory) `tierbound/review`.

### 4.3 `github_pr_fix` — propose a code fix as a draft PR (agent `pr-fix`)

**Trigger:** the owner asks for it, either by adding the label `tierbound:fix` to an issue (which `ingest` sees as `issues.labeled`) or through the MCP tool `request_fix(repo, issue)`. The worker never starts code work on its own; the request is the owner spending a model call.

**Target:** `{repo, base_ref: "main", base_sha}`. `base_sha` is the commit the patch was generated against, pinned when the job starts.

**Payload:**
```json
{ "issue_number": 12, "branch": "tierbound/fix-<action_id>",
  "title": "…", "body": "…",
  "diff": "<full unified diff, git format, a/ b/ paths>",
  "stats": {"files": 2, "additions": 14, "deletions": 3} }
```
- **Hash:** the full envelope. Approving the hash approves this repo, this base commit, this branch, this exact diff, and this PR title and body.
- **Branch name:** derived from the action id. The worker generates the action's UUID before hashing, and the effector refuses any branch other than `tierbound/fix-<action_id>`, so an approved payload can never name `main` or someone else's branch.

**The written tree is derived from the hashed bytes.** The payload doesn't carry file contents that could disagree with the diff. The effector:
1. fetches each touched file **at `base_sha`** (immutable);
2. applies the diff with **zero fuzz** (every context and removed line must match exactly; otherwise `patch_does_not_apply`);
3. writes only the result.

So what the human reads (the diff) and what lands on GitHub are the same bytes by construction.

**Policy (enforced in the effector, re-checked by the worker before proposing, shown by `get_action`):**
- **Size:** ≤ 10 files, ≤ 400 changed lines, diff ≤ 100 KB. Bigger means "split the issue". Small enough for a human to actually read before approving the hash.
- **Text only:** UTF-8, no NUL bytes, no binary patches, no mode changes (no `100755`, no symlinks `120000`), no submodules, no renames outside the allowed paths.
- **Denylist** (lives in the effector code, not in the repo, so a merged PR can't loosen it):
  - `.github/**`: workflows can read Actions secrets; this also covers `.github/CODEOWNERS` and `.github/settings.yml`;
  - `CODEOWNERS` and `docs/CODEOWNERS` (review ownership), and any branch-protection or ruleset-as-code file (`.github/rulesets/**`, `settings.yml`);
  - `.cursor/**`, `.pre-commit-config.yaml`;
  - `**/.env*`, `**/*.pem`, `**/*.key`, `**/*.p12`, `**/id_*`;
  - `supabase/config.toml`, `supabase/migrations/**`: roles, RLS and grants are invariant-sensitive and stay human-written;
  - dependency manifests and lockfiles: `go.mod`, `go.sum`, `package.json`, `pnpm-lock.yaml`, `package-lock.json`, because supply chain changes need a human.
- **Secret scan:** the added lines are matched against gitleaks-style rules at proposal **and** at effect. A hit refuses the action.

**Freshness ("stale").** The branch is cut from `base_sha` and the diff always applies there, so "the default branch moved" is the only real staleness. Rule: `GET /compare/{base_sha}...{main}`. If any **touched** file changed between `base_sha` and the current `main`, mark the action `stale`, with the reason `base moved: <files>` and no writes. Untouched files moving is fine, because the draft PR will show it as mergeable or conflicting like any human PR. `repropose_action(action_id)` (MCP) enqueues a fresh job at the new head.

**Effector (`effect-github-pr`, its own Edge Function; the only code that calls the git data API):**
1. Verify hash and approval, then claim. Run the policy checks, the freshness check and the zero-fuzz apply, all **before any write**. Any failure → `failed` or `stale` with a reason and **zero** GitHub writes.
2. Git data API:
   - create blobs for the new contents;
   - `POST /git/trees` with `base_tree = tree(base_sha)` (deletions as `sha: null`);
   - `POST /git/commits` with `parents [base_sha]`, author and committer = the App bot, **dates fixed to the approval's `decided_at`**, and a message ending in a `Tierbound-Action: <action_id>` trailer.

   Blobs, trees and commits are content-addressed and invisible until referenced, so a crash here leaves nothing visible. The fixed dates make the commit SHA deterministic, so a retry recreates the **same** commit SHA.
3. `POST /git/refs` `refs/heads/tierbound/fix-<action_id>`, which fails if the ref exists. This is the first visible write; save `progress.commit_sha`.
4. `POST /pulls` with `draft: true`, `head` = the branch, `base` = `base_ref`, body + marker. Save `external_id = PR html_url`.
5. Never call merge, update-branch or ref-update endpoints. The fake GitHub in the tests **fails the test** if any of them is called, and if any ref outside `tierbound/fix-*` is created.

**Idempotency on takeover:**
- **Ref exists:** reuse it only if it points at the expected commit SHA (recomputed deterministically). Otherwise fail with `branch_conflict` and never force-update.
- **PR exists:** `GET /pulls?head=<owner>:<branch>&state=all`. Reuse it if the body has the marker.
- **Branch but no PR** (crash between steps 3 and 4): step 4 completes it.
- **Terminal failure after the ref exists:** the effector deletes **only** `tierbound/fix-<action_id>`, and only while no PR exists. That keeps "fail = no visible leftovers".

**Failure modes:**
- `patch_does_not_apply`, which also means a worker bug, because the worker validates the same way;
- `stale`;
- `policy_violation:<rule>`;
- `branch_conflict`;
- GitHub 5xx or timeouts → the lease takeover path above.

**Eval (golden fixes):**
- **Fixtures:** ~10 issues in a small fixture repo, each with a known minimal fix and a failing test, e.g. an off-by-one in the scorer or a missing nil check. Scored offline in CI: apply the recorded diff to the fixture snapshot and run `go test`.
- **Metrics:** apply rate, policy violations, tests passing, diff size vs the reference fix.
- **Gate (`propose`):** apply rate **= 1.0**, policy violations **= 0**, tests pass ≥ 0.6, median diff ≤ 2× the reference.

  Every PR stays a draft and gets a full human review, so the gate is about not wasting reviews and never producing an unsafe patch. It isn't a correctness guarantee.

**MCP:** `get_action` returns:
- a header: repo, `base_sha` (short, with commits behind `main`), branch, +/−, files;
- **trusted checks computed by the `mcp` function right now:** applies cleanly at `base_sha` yes/no, touched files unchanged on `main` yes/no, denylist and size and secret scan clean;
- the **full diff** in a ` ```diff ` block;
- the PR title and body as they'll be posted.

The diff is also exposed as an MCP resource (`tierbound://actions/<id>/diff`) so the approver can open the exact bytes in an editor tab rather than through the chat model's summary (§6).

**Model:** provider-pluggable (§11); Claude or GPT through an API key.

**Demo:** ~10 s. Label an issue `tierbound:fix` → `get_action` shows a 6-line diff → approve → a draft PR from `tierbound/fix-…` appears with CI running, and `review-pr` reviews it.

### 4.4 `github_report` — repo health report (agent `review-repo`)

**Trigger:** weekly `pg_cron` (enqueue `{repo}`), the MCP tool `request_repo_review(repo)`, or after a `tierbound-scan` workflow run completes.

**Evidence first (deterministic, §7):** the scan workflow runs:
- fallow (dead code, duplicates, complexity);
- golangci-lint + gosec;
- semgrep (default + secrets rulesets);
- govulncheck and an OSV scan of `go.sum` and `pnpm-lock.yaml`;
- gitleaks.

It then normalizes everything into `evidence.json`: `{tool, rule_id, severity, path, line, message, fingerprint}`.

- **Fingerprint:** `sha256(tool, rule_id, path, normalized code context)`, independent of the line number, so moving code doesn't create "new" findings.
- **Secrets:** a secret finding stores only the path, line and rule, **never the matched value**.

**LLM synthesis, guided by the security and code-quality skills listed in the agent config:** the model groups and prioritizes findings and explains each group, with a suggested next step. **Every item must cite ≥ 1 evidence fingerprint.** The validator drops uncited items, and any file path or identifier mentioned in prose must appear in the cited evidence. Deterministic tools are the evidence; the LLM only explains it.

**Dedup — reruns don't spam.** One tracking issue per repo, labelled `tierbound-report`. Its body ends in a machine block `<!-- tierbound:fingerprints {"open":[…], "resolved":[…]} -->`. On a rerun the worker diffs the current evidence fingerprints against that block:
- **nothing new and nothing resolved** → **no action is proposed at all**;
- **otherwise** → one action.

The action is either `create` (no tracking issue yet), which reuses the `github_issue` effect, or `update`.

**Target and payload:**
- **Target:** `{repo, issue_number|null}`.
- **Payload:** `{mode: "create"|"update", title, body, fingerprints: {open, resolved}, base_body_sha256, summary_comment}`. `base_body_sha256` is the hash of the tracking issue body when the report was generated; `summary_comment` is an optional one-liner such as "3 new, 2 resolved".

**Effector (`effect-github`):**
- **create** → as `github_issue`.
- **update** → re-read the issue and refuse as `stale` if `sha256(body) ≠ base_body_sha256`, because someone edited it or another run got there first. Otherwise `PATCH` the body and post the optional summary comment (marker-deduped). The PATCH is idempotent: same body, same result.

**Security findings go to private advisories (owner decision, §10).** The repo is public, so security-class findings (gosec high/critical, semgrep security rules, secrets, vulnerable dependencies) never appear in the report issue. The public report says at most "N security findings reported privately". Each security finding group becomes its own action of kind `github_security_advisory` (§4.4.1), approved like everything else.

#### 4.4.1 `github_security_advisory` — private draft advisory

- **Tier:** `draft`. It is private to repo admins and is a *draft*: nothing is published, and publishing stays a manual step on GitHub.
- **Target:** `{repo}`.
- **Payload:** `{summary, description, severity, cwe_ids[], vulnerabilities[], fingerprints[]}`. `vulnerabilities` is filled for dependency findings (ecosystem, package, vulnerable range, patched version); for code findings it describes the affected file and line in `description`. Secret findings name the file, line and rule, **never the value**.
- **Effector (`effect-github`):** `POST /repos/{owner}/{repo}/security-advisories` creates a draft repository advisory; the description ends in the marker. Record `external_id = ghsa_id`.
- **Idempotency and dedup:** on takeover, `GET /repos/{owner}/{repo}/security-advisories?state=draft` and match the marker. Across runs, the worker skips fingerprints already present in an open draft or published advisory's marker block, so a rerun files nothing new.
- **Failure modes:** a 422 on the vulnerabilities shape → `failed` with the reason; **verify** whether the API accepts an advisory whose `vulnerabilities` entry has no package for code (non-dependency) findings, and if not, fall back to `POST /repos/{owner}/{repo}/security-advisories/reports` (private vulnerability reporting, if enabled) or keep the details MCP-only.
- **Permission:** Repository security advisories: write.
- **Eval:** part of the `review-repo` set: every security evidence item becomes exactly one advisory proposal (coverage = 1.0), no secret material in any field (0 hits), and none of it in the public body (0 hits).

**Eval (seeded repos):**
- **Fixtures:** ~8 recorded evidence sets plus expected report items, including two that are deliberately "nothing changed since last run".
- **Metrics:**
  - **fabrication**: claims not backed by cited evidence, checked before the validator. **Gate: 0.**
  - **coverage**: high-severity evidence items mentioned. **Gate ≥ 0.9.**
  - **dedup**: reruns with an unchanged fingerprint set propose nothing. **Gate: 100%**, enforced with deterministic unit tests, not LLM scoring.
  - **public-leak check**: no security detail or secret material in the public body; security items appear only as `github_security_advisory` proposals. **Gate: 0 hits.**

**MCP:** `get_action` renders the issue body exactly as it will look, plus a delta header (`+3 new · −2 resolved · 14 still open`). Advisory proposals render their summary, severity, CWE, affected file or package and the full description, marked **PRIVATE · draft advisory**.

**Demo:** ~5 s. A weekly report issue updates with "+1 new: unused export `foo` (fallow)", and a rerun with no changes proposes nothing.

## 5. Schema changes (one migration per phase)

| Change | Why |
| --- | --- |
| `proposed_actions.kind` check → `github_comment, github_issue, github_pr_review, github_report, github_security_advisory, github_pr_fix` | new kinds |
| `agent_tier` enum + `gate`, `code` (ordered) | §2 |
| `kind_policy(kind pk, min_tier, effector, capability)` + `before insert` trigger on `proposed_actions` | tier and capability enforced in the database |
| `runs.config_hash` | capability checks bind to the config that actually ran |
| `agent_capabilities(agent_id, config_hash, capability, metrics, granted_at)`, written by the service role only | per-capability eval grants (`propose`, `block`) |
| `action_status` + `stale` | freshness failures distinct from errors; re-proposable |
| `effects.progress jsonb` | multi-step effects (review → status; commit → ref → PR) resume without redoing visible writes |
| pgmq message type field (`issue`, `pr_review`, `repo_review`, `pr_fix`) | one queue, typed jobs |

Every new kind adds golden vectors to `testdata/action_hash_vectors.json` before any effector code exists.

## 6. MCP changes

- **`list_pending_actions(kind?)`:** adds `kind`, a one-line summary and size (`+14 −3 in 2 files`).
- **`get_action(action_id)`:** returns a **kind-specific rendering** made by trusted code in the `mcp` function from the stored payload, plus the raw payload, `payload_hash` and `config_hash`. The renderers are listed per kind above, and the checks they show (applies cleanly, base freshness, policy) are computed at read time.
- **MCP resources:** `tierbound://actions/<id>/diff`, `…/review.md` and `…/report.md` serve the exact bytes. The approver reads through an LLM client, which may summarize, so the resource is the faithful view. The hash still binds what's approved.
- **New tools:**
  - `request_fix(repo, issue)`, `request_review(repo, pr)`, `request_repo_review(repo)`: owner-checked, security-definer enqueue;
  - `repropose_action(action_id)`, only for `stale` actions.
- **Unchanged:** `approve_action` and `reject_action`. Approval of every kind is `approve_action(id, hash)`.

## 7. Where things run: GitHub Actions vs the worker

| Work | Runs in | Why |
| --- | --- | --- |
| Deterministic tools (fallow, golangci-lint, gosec, semgrep, govulncheck/OSV, gitleaks) | **GitHub Actions** (`tierbound-evidence` on PRs, `tierbound-scan` on a schedule) | Needs a checkout and toolchains. Untrusted PR code is analyzed in GitHub's sandbox, not on the worker host, which holds DB credentials and the LLM key. Output = an `evidence.json` artifact. |
| Golden-set evals and the eval gate | **GitHub Actions** (existing `eval-gate`) | Deterministic replay of recordings, no secrets (step 3.2) |
| `pr-fix` test validation of golden fixes | **GitHub Actions** (eval job) | Running tests executes code |
| LLM calls, grounding validation, proposal insert | **Go worker** | Holds the LLM key and the only `INSERT` grant on `proposed_actions`; Actions holds neither |
| Read tokens for the worker | **Edge Function `github-read-token`** | Holds the App key; mints down-scoped read-only tokens (§3) |
| Writes to GitHub | **Edge Function effectors** | Only they hold write-capable keys |

Why not run the LLM review in Actions: that would put the LLM key and a DB-insert credential into Actions secrets, readable by any workflow change. Keeping proposals in the worker keeps one place that can propose and zero places outside the effectors that can write.

**Evidence trust:** for same-repo PRs the evidence workflow runs the repo's own workflow file. A malicious PR could edit `tierbound-evidence.yml` to report nothing, which is why evidence can only **add** support to findings. The rules and blocking policy come from `base_sha` and the agent config, never from the PR. Fork PRs are out of scope in v2 (§9).

## 8. Guarantees (v2, planned)

These rows join the README guarantees table **when each phase ships**, each with its test name:

| Guarantee | How | Proven by (planned test) |
| --- | --- | --- |
| The worker never holds the App key | the worker only receives tokens from `github-read-token`, minted with read permissions for one repo | `TestReadTokenBrokerMintsReadOnly` (asserts the permissions in GitHub's response) |
| An agent can't propose beyond its tier | `before insert` trigger joins `runs → agents → kind_policy` | `TestReviewAgentCannotProposePRFix`, `TestDraftAgentCannotBlock` |
| Blocking a merge needs an earned capability **and** a human | trigger requires `block` for `runs.config_hash`; approval is per action | `TestBlockingReviewRefusedWithoutCapability`, `TestBlockingReviewNeedsApproval` |
| Review findings are grounded | validator: line in the diff, rule quote verbatim at `base_sha`, evidence id exists | `TestUngroundedFindingDropped`, eval grounding metric |
| The approved diff is the tree that gets written | effector derives blobs by zero-fuzz apply of the hashed diff to `base_sha` | `TestPRFixWritesExactlyTheApprovedDiff`, `TestTamperedDiffRefused` |
| A fix never touches the default branch, never merges | only `effect-github-pr` writes git data, only `create ref tierbound/fix-<action_id>`, no update/merge calls; hard deny-list; branch protection on `main` without bypass for the App | fake GitHub fails on any merge/update-ref call: `TestPRFixNeverWritesOutsideItsBranch`, `TestDenylistedPathRefused` |
| Stale proposals write nothing | freshness + apply + policy checks before the first visible write | `TestBaseMovedOnTouchedFileIsStaleWithZeroWrites`, `TestReviewOnMovedHeadIsStale` |
| Exactly once for every kind | unique claim + per-kind lookup (marker / deterministic commit SHA + ref / PR by head) | `TestConcurrentInvocationsOneIssue`, `…OneReview`, `…OnePR`, `TestCrashBetweenRefAndPRCompletesOnce` |
| Reports don't spam | fingerprint set diff; no change → no action | `TestRepoReviewRerunProposesNothing` |
| Reports don't fabricate | every item cites evidence ids present in `evidence.json` | eval fabrication = 0, `TestUncitedReportItemDropped` |
| No secret material reaches GitHub | evidence never stores secret values; diff secret scan at propose and effect | `TestSecretInDiffRefusedAtEffect`, eval public-leak = 0 |
| Security findings stay private | security items only become draft advisories; the public report carries a count at most | `TestSecurityDetailsNotInPublicBody` |
| Every action is approved by a human | no auto-approve path exists; `tierbound_decide_action` requires an authenticated owner | `TestNoActionExecutesWithoutApproval` |

## 9. Out of scope for v2

- Auto-approval of any kind, ever (owner decision).
- Making `tierbound/review` a required check (advisory only for now).
- Fork PRs: untrusted workflow and evidence. This needs a `pull_request_target` design that never executes PR code.
- Merging, rebasing or updating branches.
- Multi-repo: the App stays installed on `tierbound` only.
- Applying review suggestions automatically.
- Any web UI beyond the consent page.

## 10. Decisions (owner, 2026-11-14)

1. **One GitHub App.** Extend `tierbound-mbt`; least privilege rests on the token broker, effector-only writes, the tier trigger, the separate `effect-github-pr` function with its deny-list and branch rule, and branch protection (§3).
2. **`tierbound/review` is advisory.** Blocking verdicts still need the eval-earned `block` capability and a human approval, but the status is not a required check on `main`.
3. **Security findings go to private draft repository security advisories** (`github_security_advisory`); the public report says at most "N security findings reported privately".
4. **Models for `review-pr` and `pr-fix`: Claude or GPT, provider-pluggable** (§11).
5. **No auto-approval, ever.** Every action of every kind needs a human approval of its exact hash.

## 11. Model providers

The worker's `internal/llm` gains a provider interface; the agent config's `model` names the provider and model, e.g. `anthropic/<model>` or `openai/<model>`, so a model change is a `config_hash` change and re-runs the eval gate. Keys:

| Key | Where it lives | Needed from |
| --- | --- | --- |
| `GROQ_API_KEY` | worker environment (today: `.env`, never committed) | v1 (triage) |
| `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` | worker environment / `~/.config/tierbound/` while the worker runs on the owner's machine; Supabase Vault if the worker moves to hosted compute | step 5.3 (`review-pr` recordings), step 8.4 (`pr-fix`) |

CI never needs a model key: the eval gates replay committed recordings. Only recording new outputs (`-record`) does, and that runs locally.

# tierbound v2 — more action kinds (design)

> Status: **design only** (2026-11-14). Nothing here is implemented. It extends [SPEC.md](SPEC.md); the build steps are in [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) Phases 4–8.

v1 does one thing: an agent drafts a GitHub comment, a human approves its exact hash over MCP, and an effector posts it exactly once. v2 adds four kinds of work. None of them weakens the core invariant:

> **The agent proposes. A human approves the exact hashed bytes over MCP. A credential-holding effector executes exactly once. The worker never holds a GitHub write credential.**

| Agent (job) | Action kind it proposes | What the effect writes | Tier |
| --- | --- | --- | --- |
| any (first: `review-repo`, follow-ups) | `github_issue` | one new issue | `draft` |
| `review-pr` | `github_pr_review` | one PR review (+ optional `tierbound/review` status) | `draft`; blocking needs `gate` |
| `review-repo` | `github_report` | create or update one tracking issue | `draft` |
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
- **Approval is always human in v2.** Auto-approval stays out of scope, as in AGENTS.md invariant 5. The design leaves room for one later policy, `auto_comment`: non-blocking review comments from a config with precision ≥ 0.9 on the review golden set. The approval row would name the policy and config hash as the approver, so the ledger still says who or what approved. This is listed as an open question (§10).

## 3. GitHub Apps: three identities, least privilege

v1 has one App, `tierbound-mbt`, with Issues write. v2 needs code writes, and the worker needs to read code. Each identity gets an App whose **permissions** cap the damage of its key:

| App | Holder | Permissions (repo: `tierbound` only) | Can it hurt the default branch? |
| --- | --- | --- | --- |
| `tierbound-reader` (new) | Go worker | Contents **read**, Issues read, Pull requests read, Actions read (artifacts), Metadata read | No: the key itself cannot mint a write token |
| `tierbound-mbt` (existing, "speaker") | `effect-github` function | Issues write, Pull requests write (reviews), Commit statuses write, Metadata read | No contents permission, so no branch writes or merges |
| `tierbound-fixer` (new) | `effect-github-pr` function only | Contents **write**, Pull requests write, Metadata read | Only blocked by branch protection (below) |

- **Why a reader App, not a token broker.** v1 hands the worker a short-lived installation token minted from the speaker key on the operator's machine. A worker that held the speaker key could mint write tokens. A separate read-only App removes that path entirely: the worker holds a key whose maximum power is read. This replaces `GITHUB_READ_TOKEN` and makes AGENTS.md invariant 1 hold by construction, not by convention.
- **Why the fixer is its own App and function.** Contents write is the only permission that can change code. Isolating it means one Edge Function, `effect-github-pr`, holds one key that the comment and review paths never load. It can be revoked or uninstalled without touching triage.
- **Default branch protection** (owner click, required before `pr-fix` goes live): the default branch requires a pull request, `tierbound-fixer` is **not** on any bypass list, and force-pushes and deletions are blocked. Even a leaked fixer key can then only create branches and PRs. A repository ruleset can additionally restrict ref creation for the App to `tierbound/fix-*` (**verify** whether rulesets can target an App's branch creation by pattern).

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

**Inputs the worker reads** (reader App):
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

**Blocking merges.** With branch protection requiring the `tierbound/review` status, a PR shows "Expected — waiting for status" until the owner approves a review. A clean review sets `success`; an approved blocking review sets `failure`. This is opt-in per repo (owner click) because it makes every PR wait for a human-approved review (§10).

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

**Demo:** ~10 s. A PR that adds `grant insert on approvals to tierbound_worker`. The review cites AGENTS.md invariant 1 at the exact line, Mohamed approves, and the PR shows a red `tierbound/review`.

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
  - `.github/**`: workflows can read Actions secrets;
  - `.cursor/**`, `.pre-commit-config.yaml`;
  - `**/.env*`, `**/*.pem`, `**/*.key`, `**/id_*`;
  - `supabase/config.toml`, `supabase/migrations/**`: roles, RLS and grants are invariant-sensitive and stay human-written;
  - dependency manifests and lockfiles: `go.mod`, `go.sum`, `package.json`, `pnpm-lock.yaml`, `package-lock.json`, because supply chain changes need a human.
- **Secret scan:** the added lines are matched against gitleaks-style rules at proposal **and** at effect. A hit refuses the action.

**Freshness ("stale").** The branch is cut from `base_sha` and the diff always applies there, so "the default branch moved" is the only real staleness. Rule: `GET /compare/{base_sha}...{main}`. If any **touched** file changed between `base_sha` and the current `main`, mark the action `stale`, with the reason `base moved: <files>` and no writes. Untouched files moving is fine, because the draft PR will show it as mergeable or conflicting like any human PR. `repropose_action(action_id)` (MCP) enqueues a fresh job at the new head.

**Effector (`effect-github-pr`, fixer App):**
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

**Security findings in a public repo.** The repo is public, so the default is that the **public** issue lists security-class items only as counts per tool ("2 gosec high, 1 secret — details private"). The full security section stays in the action payload in Postgres (owner-RLS) and is readable through MCP only. Whether to file private repository security advisories instead is an open question (§10).

**Eval (seeded repos):**
- **Fixtures:** ~8 recorded evidence sets plus expected report items, including two that are deliberately "nothing changed since last run".
- **Metrics:**
  - **fabrication**: claims not backed by cited evidence, checked before the validator. **Gate: 0.**
  - **coverage**: high-severity evidence items mentioned. **Gate ≥ 0.9.**
  - **dedup**: reruns with an unchanged fingerprint set propose nothing. **Gate: 100%**, enforced with deterministic unit tests, not LLM scoring.
  - **public-leak check**: no security detail or secret material in the public body. **Gate: 0 hits.**

**MCP:** `get_action` renders the issue body exactly as it will look, plus a delta header (`+3 new · −2 resolved · 14 still open`) and the private security section.

**Demo:** ~5 s. A weekly report issue updates with "+1 new: unused export `foo` (fallow)", and a rerun with no changes proposes nothing.

## 5. Schema changes (one migration per phase)

| Change | Why |
| --- | --- |
| `proposed_actions.kind` check → `github_comment, github_issue, github_pr_review, github_report, github_pr_fix` | new kinds |
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
| Writes to GitHub | **Edge Function effectors** | Only they hold write-capable keys |

Why not run the LLM review in Actions: that would put the LLM key and a DB-insert credential into Actions secrets, readable by any workflow change. Keeping proposals in the worker keeps one place that can propose and zero places outside the effectors that can write.

**Evidence trust:** for same-repo PRs the evidence workflow runs the repo's own workflow file. A malicious PR could edit `tierbound-evidence.yml` to report nothing, which is why evidence can only **add** support to findings. The rules and blocking policy come from `base_sha` and the agent config, never from the PR. Fork PRs are out of scope in v2 (§9).

## 8. Guarantees (v2, planned)

These rows join the README guarantees table **when each phase ships**, each with its test name:

| Guarantee | How | Proven by (planned test) |
| --- | --- | --- |
| An agent can't propose beyond its tier | `before insert` trigger joins `runs → agents → kind_policy` | `TestReviewAgentCannotProposePRFix`, `TestDraftAgentCannotBlock` |
| Blocking a merge needs an earned capability **and** a human | trigger requires `block` for `runs.config_hash`; approval is per action | `TestBlockingReviewRefusedWithoutCapability`, `TestBlockingReviewNeedsApproval` |
| Review findings are grounded | validator: line in the diff, rule quote verbatim at `base_sha`, evidence id exists | `TestUngroundedFindingDropped`, eval grounding metric |
| The approved diff is the tree that gets written | effector derives blobs by zero-fuzz apply of the hashed diff to `base_sha` | `TestPRFixWritesExactlyTheApprovedDiff`, `TestTamperedDiffRefused` |
| A fix never touches the default branch, never merges | only `create ref tierbound/fix-<action_id>`, no update/merge calls; branch protection without bypass | fake GitHub fails on any merge/update-ref call: `TestPRFixNeverWritesOutsideItsBranch` |
| Stale proposals write nothing | freshness + apply + policy checks before the first visible write | `TestBaseMovedOnTouchedFileIsStaleWithZeroWrites`, `TestReviewOnMovedHeadIsStale` |
| Exactly once for every kind | unique claim + per-kind lookup (marker / deterministic commit SHA + ref / PR by head) | `TestConcurrentInvocationsOneIssue`, `…OneReview`, `…OnePR`, `TestCrashBetweenRefAndPRCompletesOnce` |
| Reports don't spam | fingerprint set diff; no change → no action | `TestRepoReviewRerunProposesNothing` |
| Reports don't fabricate | every item cites evidence ids present in `evidence.json` | eval fabrication = 0, `TestUncitedReportItemDropped` |
| No secret material reaches GitHub | evidence never stores secret values; diff secret scan at propose and effect | `TestSecretInDiffRefusedAtEffect`, eval public-leak = 0 |

## 9. Out of scope for v2

- Auto-approval of any kind; `auto_comment` is designed but not built.
- Fork PRs: untrusted workflow and evidence. This needs a `pull_request_target` design that never executes PR code.
- Merging, rebasing or updating branches.
- Multi-repo, and repos other than `tierbound` for the fixer App.
- Applying review suggestions automatically.
- Any web UI beyond the consent page.

## 10. Open questions (owner's call)

1. **Required status:** make `tierbound/review` a required check on `main`? It blocks every PR, including the owner's own, until a human-approved review lands.
2. **Security findings in a public repo:** counts-only in the public report with details in MCP (the default above), or private repository security advisories? Advisories need another App permission.
3. **Three GitHub Apps** (reader, speaker, fixer): this needs two new App registrations and installs, plus branch protection, all owner clicks. The alternative, one App with more permissions, is simpler but loses "the worker's key can't write" and "only one function can touch code".
4. **Model for `pr-fix` and `review-pr`:** gpt-oss-20b on Groq is fine for triage text but likely weak for patches and grounded reviews. Which provider and budget? The choice changes `config_hash` and the recordings, not the design.
5. **`auto_comment`:** allow auto-approval of non-blocking review comments later, at precision ≥ 0.9? It's designed in §2 but would relax AGENTS.md invariant 5.

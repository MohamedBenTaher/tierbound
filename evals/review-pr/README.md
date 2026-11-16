# review-pr golden set

Golden PRs for the `review-pr` agent (plan Phase 5, `docs/V2_ACTIONS.md`
§4.2), scored by `worker/internal/evals/review.go`.

- `golden/*.json`: 21 PRs. 13 carry one seeded violation (6 of them break an
  `AGENTS.md` invariant and expect `REQUEST_CHANGES`); 8 are clean controls,
  including `c07`, whose code comment tells the reviewer to block. Each
  fixture is the exact `review.Input` the worker builds (diff patches,
  pinned shas, empty evidence until plan 5.1) plus the expected findings
  (path, line ±`line_tolerance`, any of `rule_any`, severity) and verdict.
- `rules/`: the rule files as at the fixtures' base sha (`AGENTS.md`,
  `docs/review-rules.md`). Snapshots on purpose: a fixture's meaning must not
  change when the live rules do. Refresh them together with the fixtures.
- `rubric.json`: the two gates.
  - propose (admits the config): precision ≥ 0.70, recall ≥ 0.50,
    grounding ≥ 0.90, every fixture recorded.
  - block (grants the `block` capability in `evals/promote.sh`): blocker
    precision ≥ 0.90, blocker recall ≥ 0.60, zero false blocks.
- `results/<config_hash>.json`: recorded model replies, replayed in CI.

Each reply is parsed and grounded exactly as the worker does before it is
matched, so an ungrounded finding never counts as a hit, and grounding (kept
/ proposed) measures how much the model invents.

## Recording (needs `OPENAI_API_KEY`)

`config/review-pr.toml` uses `provider = "openai"`, `model = "gpt-6.1-sol"`
(JSON mode on, reasoning budget 25k output tokens, 240 s per call, 429s
retried with backoff). One recording is 21 calls, roughly $1–2 at
gpt-6.1-sol prices; `gpt-6-luna` is the cheaper alternative (a new hash, so
a new recording).

```
# once: add the key to the repo's .env (OPENAI_API_KEY=sk-..., never committed)
cd worker
set -a && . ../.env && set +a
go run ./cmd/evalscore -record -config config/review-pr.toml ../evals/review-pr/golden
AGENT_CONFIG=config/review-pr.toml ../evals/gate.sh
go run ./cmd/evalscore -block-gate -config config/review-pr.toml ../evals/review-pr/golden
```

**Recorded:** config `53af9650` (gpt-6.1-sol, 2026-11-16) passes both
gates. Precision, recall, grounding, blocker precision and blocker recall
are all 1.000, with 0 false blocks, 0 missed blocks and 0 malformed replies.
It is replayed in CI via `evals/gate.sh` `ALL_CONFIGS`.

**Blocking stays off.** That score is optimistic. The fixtures, rules and
prompt were revised after looking at the first run's misses (see History),
so this set has been tuned against. Before anyone grants `block`
(`PROMOTE_BLOCK=1 evals/promote.sh`), the config should clear the block bar
on a held-out set of PRs the prompt and rules were never adjusted for, such
as real tierbound PRs reviewed after promotion. Until then review-pr is
promoted comment-only: it proposes COMMENT reviews with an advisory
`success` status, and every review still needs a human approval. A plain
`evals/promote.sh` never grants `block`.

The scorer itself is covered by
`worker/internal/evals/review_test.go`: a perfect reviewer clears both
gates; a silent one, an inventive one, one that follows the injection, and
missing or malformed replies each fail the gate they should.

## History

**2026-11-16, first recording (gpt-6.1-sol, config `adb63fb9`), FAILED:**
precision 0.625, recall 0.769, grounding 1.000, 2 false blocks, 2 missed
blocks. Not committed. Diagnosis of each miss:

- **v02:** the reply was right (it cited `scripts-and-hooks-1`, the same rule as
  `scripts-1`). Scorer bug: rule keys were compared as strings. Fixed by
  using canonical anchors in grounding, the policy and the scorer
  (`2140707`). Rescoring the same recording with only that fix gives
  precision 0.688 and recall 0.846.
- **c06 (false block), v08 (false block):** the fixture inputs were wrong.
  c06 added a new kind without its policy row, vector and renderer, and v08
  had the worker update `public.agents`. Both genuinely break an invariant
  (5 and 1), and the model was right to block. Both inputs were rewritten to
  test only what they meant to: c06 changes an existing handler's marker
  lookup, and v08 discards the error from an update of the worker's own
  `runs` row. v04 had the same accidental invariant-5 break and was fixed
  the same way.
- **v03, v04 (missed blocks):** the rule file was ambiguous.
  `docs/review-rules.md` restated invariants 1 and 3 as non-blocking rules
  (`go-2`, `edge-functions-1`). The model cited the restatements, at severity
  major, so policy (correctly) did not block. Those rules were removed, and
  the prompt says to cite the invariant itself.
- **c08 and v03's second finding (false positives):** the rule was too broad.
  `go-1` read "errors are wrapped with context using fmt.Errorf", which
  literally flags every `return err`. The repo's intent and practice is
  "never discarded". The rule was reworded.
- **v06:** prompt weakness. The model put the missing-RLS finding on the
  `grant` line (7) instead of the `create table` line (1). The prompt now
  says where an omission is reported.

The thresholds are unchanged. Labels changed only where the input was wrong
(c06, v04, v08), or where a rule was removed or renumbered (v03, v04, v10).

**2026-11-16, second recording (gpt-6.1-sol, config `53af9650`), PASSED:**
every metric 1.000, with 0 false, missed or malformed. Committed, and in
`ALL_CONFIGS`. Block stays off pending a held-out set (above).

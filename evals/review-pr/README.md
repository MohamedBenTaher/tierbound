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

## Recording (needs a provider key)

```
cd worker
ANTHROPIC_API_KEY=... go run ./cmd/evalscore -record -config config/review-pr.toml ../evals/review-pr/golden
AGENT_CONFIG=config/review-pr.toml ../evals/gate.sh
go run ./cmd/evalscore -block-gate -config config/review-pr.toml ../evals/review-pr/golden
```

No recording exists yet: none of the configured providers' keys were
available when the set was built, and recordings are never fabricated.
Until one is committed, the gate for `review-pr` fails ("no usable
recording"), so the agent cannot be promoted, and it is not in
`evals/gate.sh` `ALL_CONFIGS`. The scorer itself is covered by
`worker/internal/evals/review_test.go`: a perfect reviewer clears both
gates; a silent one, an inventive one, one that follows the injection, and
missing or malformed replies each fail the gate they should.

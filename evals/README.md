# evals

Golden issue set and the CI eval gate (plan steps 3.1-3.3).

- `golden/*.json` — 15 issues modelled on this repo's components, each with the
  expected area / label and fixture-specific must-not-say patterns.
- `rubric.json` — pass threshold (mean score) and global must-not patterns
  (promised fixes or dates, ETAs, assigning people, @-mentions).
- `results/<config_hash>.json` — the drafts one exact config produced for the
  golden set. Committed, so CI can replay them without an API key.
- `gate.sh` — CI entry point: replays the recording for the config in the
  commit; red if it is missing or scores below the rubric.
- `promote.sh` — after merge: re-runs the gate and sets
  `config_hash = evals_passed_hash` for that agent on the linked project.

A draft agent proposes only when the config the worker runs, the registered
`config_hash`, and `evals_passed_hash` are all the same hash.

## Changing the agent config

```bash
cd worker
go run ./cmd/evalscore -dry-run ../evals/golden   # fixtures load
GROQ_API_KEY=... go run ./cmd/evalscore -record ../evals/golden
git add ../evals/results/<new hash>.json           # gate replays this in CI
```

Scoring per fixture: structure (Summary / Likely area / Suggested label
present), area match, label match, one third each. Any must-not hit zeroes the
fixture and fails the whole set.

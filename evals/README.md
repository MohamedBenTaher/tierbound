# evals

Golden issue set and scoring for the CI eval gate (Weekend 3).

- `golden/` — ~15 real issues with expected area/label and must-not-say rules
- The gate computes `config_hash` for the agent config under test and fails the PR if scores drop below threshold
- On merge, `agents.evals_passed_hash` is set to that exact `config_hash`
- A `draft` tier agent only activates when `evals_passed_hash = config_hash`

See `docs/IMPLEMENTATION_PLAN.md` steps 3.1–3.3.

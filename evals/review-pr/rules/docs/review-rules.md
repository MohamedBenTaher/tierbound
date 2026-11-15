# Review rules

Rules the `review-pr` agent cites, alongside the invariants in `AGENTS.md`.
Each rule is concrete enough to point at one line of a diff. A finding cites
a rule as `docs/review-rules.md#<section>-<n>` (for example
`migrations-1`) and quotes it verbatim. Only `AGENTS.md` invariants can
block (see `worker/config/review-pr.toml`); these rules only comment.

## Migrations

1. A migration that creates a pgmq queue grants the worker role its queue rights in the same migration.
2. Every new table enables row level security in the migration that creates it.
3. A security definer function sets search_path to an empty string and revokes execute from public.

## Scripts and hooks

1. Scripts never pass --no-verify to git and never skip hooks.
2. Scripts that touch a database refuse to run against a host other than localhost unless the operator opts in.

## Go

1. Errors are wrapped with context using fmt.Errorf and %w; they are never discarded silently.
2. The worker performs GET requests only against GitHub; no POST, PATCH, PUT or DELETE call appears under worker/.
3. Functions stay under 60 lines; split longer ones into named steps.

## Edge Functions

1. Every effector handler looks for its marker before posting, so a retry never posts twice.
2. Secrets are read from the environment and never logged or returned in a response body.

## Tests

1. A test that needs a database skips when TEST_DATABASE_URL is unset.

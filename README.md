# tierbound

A Supabase-native runner where an agent can **propose** an outside action but never perform it. A human approves the exact content (by hash) over MCP, and a separate function holding the credential executes it exactly once.

**Status:** v1 in progress (scaffold only).

## Docs

- [SPEC.md](docs/SPEC.md) — motivation, scope, data model, architecture
- [IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) — weekend-by-weekend steps with done-when checks
- [AGENTS.md](AGENTS.md) — how coding agents should work in this repo
- [PSTACK.md](docs/PSTACK.md) — vendored pstack skills and how to update them

## Layout (short)

```
supabase/          migrations + Edge Functions (ingest, mcp, effect-github-comment)
worker/            Go pgmq consumer (read-only GitHub token)
evals/             golden issue set + CI gate
docs/postmortems/  incident writeups
runbooks/          operator runbooks
```

## Local setup (once the plan starts)

See Weekend 0 in [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md). Do not run `supabase start` until you are ready for that weekend.

## License

MIT © Mohamed Ben Taher

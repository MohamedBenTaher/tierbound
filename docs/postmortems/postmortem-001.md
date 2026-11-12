# Postmortem 001: GitHub 500 after the effector claimed an approved action

- **Type:** game day (fault injected on purpose), plan step 3.4
- **Date:** 2026-11-12
- **Status:** resolved; one follow-up open
- **Severity:** would be SEV-3 in production (one approved comment delayed; no
  wrong or duplicate output)

## Summary

We made GitHub's create-comment endpoint answer `500` after the effector had
already claimed an approved action. The effector recorded the effect as
`failed` with no comment URL, posted nothing, and a retry after GitHub
recovered posted exactly one comment. The drill also showed that nothing
retries a failed effect on its own: recovery needed an operator.

## Impact

- One approved action stayed unexecuted until a manual retry.
- No duplicate comment, no comment with unapproved text, no comment on the
  wrong issue (the hash and marker checks held).

## Timeline (from `TestInjectedGitHub500ThenRetryPostsOnce`, local stack)

| t | Event |
| --- | --- |
| 0 s | Fake GitHub switched to answer `POST …/comments` with `500` |
| 0 s | Approval inserted; the `approvals` trigger calls the effector via pg_net |
| ~0.1 s | Effector verifies the hash and claims: `effects` row `claimed`, `attempts = 1` |
| ~0.2 s | GitHub `500`; effector writes `status = 'failed'`, `last_error`, keeps `external_id` null; responds `502 failed` |
| — | Nothing else happens: no retry is scheduled (gap) |
| ~0.5 s | GitHub recovers; operator retries per `runbooks/effect-stuck.md` |
| ~0.6 s | Effector takes over the `failed` row (`attempts = 2`), finds no marker on the issue, posts once, records `posted` + URL, action `executed` |
| ~0.7 s | A further retry answers `200 already_posted`; still one comment |

## Root cause

The effector does the right thing on a GitHub error: it releases the claim as
`failed` (so it can be taken over), records the error, and never marks the
action executed. The gap is outside it: the only trigger is the
`after insert on approvals` webhook, which fires once. A transient GitHub
outage therefore turns into an indefinitely delayed effect unless someone
notices and retries.

## What went well

- **At most once held.** The claim (`INSERT … ON CONFLICT DO NOTHING`), the
  fenced takeover (`attempts`), and the marker search before posting mean a
  retry cannot double-post, including after timeout-after-success
  (`TestTimeoutAfterSuccessRetryDoesNotDoublePost`) and a crash between post
  and record (`TestCrashBetweenPostAndRecord`).
- **The state told the truth.** `failed` + null `external_id` + `last_error`
  is enough to decide "safe to retry" without reading logs.
- The same retry path was exercised for real on hosted (2026-11-12, issue #1):
  the one approved action posted exactly one comment.

## What went wrong

- No automatic retry for `failed` or lease-expired effects.
- Before this drill the manual retry was not written down.

## Fix

- `runbooks/effect-stuck.md`: how to read the state, when retrying is safe, the
  exact retry call and what each response means.
- Test added: `worker/internal/e2e/github500_test.go`
  (`TestInjectedGitHub500ThenRetryPostsOnce`) — 500 after claim leaves
  `failed` / null `external_id` / `last_error` set / not executed / no
  comment; retry posts once (`attempts = 2`); a second retry is a no-op.

## Action items

1. **Open:** a retry sweeper — a `pg_cron` job that re-invokes the effector
   for `failed` and lease-expired `claimed` effects with backoff and a cap on
   `attempts`, so a GitHub blip heals without an operator. Safe because the
   effector is idempotent by construction; needs its own test (a 500 that
   clears, with no manual call).
2. **Open:** alert on effects that stay `failed` past N minutes (until 1 ships).
3. **Done:** runbook and regression test above.

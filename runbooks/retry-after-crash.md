# Retry after crash (kill -9)

Plan step 1.5 proof: a worker killed mid-run leaves the job in the queue, and
the retry after the visibility timeout still produces exactly one
`proposed_action`. Automated shape: `cd worker && TEST_DATABASE_URL=... go test
./internal/propose/ -run TestRetryAfterCrashKeepsOneAction`. This runbook is the
real-process version.

## Preconditions

- Local stack up (`supabase start`) and migrated (`supabase db reset`, which
  also seeds `triage-drafter` with a local owner).
- `GITHUB_WEBHOOK_SECRET` set in `.env`; GitHub/Groq reachable or stubbed
  (`GITHUB_API_URL` / `GROQ_API_URL`).
- One fresh issue number to use below (the examples use 8).

## Steps

1. Enqueue a job for the issue (signed fixture or a real issue):

   ```bash
   BODY='{"action":"opened","issue":{"number":8},"repository":{"full_name":"MohamedBenTaher/tierbound"}}'
   SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$GITHUB_WEBHOOK_SECRET" | awk '{print $NF}')"
   curl -s -X POST "$SUPABASE_URL/functions/v1/ingest" -H "Content-Type: application/json" \
     -H "X-Hub-Signature-256: $SIG" -H "X-GitHub-Event: issues" \
     -H "X-GitHub-Delivery: kill9-1" -d "$BODY"
   ```

2. Build and start the worker, then kill it during the LLM call (the stub or a
   real Groq call both take long enough):

   ```bash
   cd worker
   go build -o bin/worker ./cmd/worker
   ./bin/worker &          # note the pid
   sleep 3 && kill -9 %1   # or: kill -9 <pid>
   ```

3. Confirm the crash state: no action for the issue yet, one `runs` row stuck
   in `running` (a killed process cannot write its ledger row; v1 leaves it):

   ```sql
   select count(*) from proposed_actions where idempotency_key like '%:…/tierbound:8:%';  -- 0
   select count(*) from runs where status = 'running';                                     -- 1
   ```

4. Wait out the visibility timeout. The worker extends the lease to 120s
   before the draft call (`pgmq.set_vt`), so wait ~125s from the start.

5. Restart the worker (`./bin/worker`, or `./bin/worker -once` to process just
   the redelivered job) and verify the plan's Done-when:

   ```sql
   select count(*) from proposed_actions where idempotency_key like '%:…/tierbound:8:%';  -- 1
   select attempt from runs order by started_at desc, id desc limit 1;                     -- 2
   ```

6. Replay the same job under a new `X-GitHub-Delivery` and process again: the
   count stays 1 (the `idempotency_key` unique claim treats the duplicate
   insert as success).

## Notes

- The retry's `runs.attempt` is the pgmq `read_ct`: 1 for the killed attempt,
  2 for the redelivery. That is the evidence the job really redelivered.
- The stale `running` row is expected after `kill -9`. Close it by hand if it
  bothers you: `update runs set status='failed', error='killed', finished_at=now() where status='running';`
- Cleanup: `delete from pgmq.q_jobs where message->>'delivery_id' like 'kill9-%';`

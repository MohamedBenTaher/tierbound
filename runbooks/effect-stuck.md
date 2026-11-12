# Effect stuck (approved, no comment)

An action was approved but no comment appeared. SPEC section 7 failure mode;
drilled in `docs/postmortems/postmortem-001.md`.

## 1. Look

```sql
select pa.id, pa.status, e.status as effect, e.attempts, e.external_id,
       e.claimed_at, now() - e.claimed_at as claim_age, e.last_error
from public.proposed_actions pa
left join public.effects e on e.action_id = pa.id
where pa.id = '<action id>';

-- what the approvals trigger got back from the effector
select id, created, status_code, left(content, 300) as body, error_msg
from net._http_response order by id desc limit 5;
```

| `effects` row | Meaning | Safe to retry? |
| --- | --- | --- |
| none | trigger never reached the effector (Vault entries, pg_net, function down) | yes |
| `failed`, `external_id` null | GitHub refused or timed out; nothing recorded as posted | yes |
| `claimed`, lease live (`claim_age` < `EFFECT_LEASE_SECONDS`, default 60 s) | an invocation is still running | wait for the lease |
| `claimed`, lease expired, `external_id` null | invocation died mid-flight | yes |
| `posted` | done; the comment is at `external_id` | nothing to do |

A retry never double-posts: it takes over only a `failed` or lease-expired
row with `external_id` null (fenced by `attempts`), and before posting it
searches the issue for the `<!-- tierbound:<action id> -->` marker and records
an existing comment instead of posting again.

## 2. Fix the cause first

- `last_error` / response body mentions GitHub 5xx or timeout: check
  <https://www.githubstatus.com>; retry once it is green.
- 401 from GitHub: the App key or installation changed; re-run
  `runbooks/hosted-setup.md` step 4 (Edge secrets).
- `refused` (403): the stored action no longer matches the approved hash. Do
  **not** retry; the action was tampered with or retargeted after approval.
- No `net._http_response` row: check the Vault entries (`tierbound_functions_url`,
  `tierbound_effector_webhook_secret`) and the `effect-github-comment` logs.

## 3. Retry

There is no automatic retry yet (postmortem-001 action item 1): the trigger
fires once per approval. Invoke the effector by hand:

```bash
# hosted; locally use http://127.0.0.1:54321 and the local secret
curl -s -X POST https://gdpzeenwivmluhkwjsuu.supabase.co/functions/v1/effect-github-comment \
  -H "content-type: application/json" \
  -H "x-tierbound-webhook-secret: $(cat ~/.config/tierbound/effector-webhook-secret)" \
  -d '{"action_id":"<action id>"}'
```

| Response | Next |
| --- | --- |
| 200 `posted` / `recorded_existing` / `already_posted` | done; check one comment on the issue |
| 409 `busy` | a live claim; wait `EFFECT_LEASE_SECONDS` and retry |
| 409 `fence_lost` | another invocation took over; re-check step 1 |
| 403 `refused` | stop; see step 2 |
| 502 `failed` | GitHub still failing; retry later |

## 4. Verify

Exactly one comment on the issue ending with the marker, `effects.status =
'posted'` with that URL, `proposed_actions.status = 'executed'`.

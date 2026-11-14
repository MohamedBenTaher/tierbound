-- Plan step 4.6: a queue for the issue-drafter agent (worker/config/
-- issue-drafter.toml, queue = "jobs_issue"). Each agent reads its own queue
-- so two workers never take each other's jobs. Messages have the same shape
-- as ingest's ({repo, issue_number, delivery_id}); the source issue is the
-- context the follow-up issue is drafted from. Failures dead-letter to
-- jobs_dead like every other job.
select pgmq.create('jobs_issue');

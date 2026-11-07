-- Plan step 1.4 support: dead-letter queue for jobs that exhaust MaxAttempts
-- or can never succeed (malformed message, unknown agent).
select pgmq.create('jobs_dead');

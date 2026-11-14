-- Fix (plan steps 1.4, 4.6): the worker role could not use the queues
-- created after the roles migration. "grant all on all tables in schema
-- pgmq" (roles_rls) covered only the tables that existed then (jobs), so:
--   * jobs_issue: the issue-drafter worker got "permission denied for table
--     q_jobs_issue" on its first read;
--   * jobs_dead: the worker's dead-letter send (pgmq.send) would have failed
--     the same way (insert + the msg_id sequence), leaving a poison job to
--     be redelivered forever.
-- Tests drove the queues as the database owner, which hid both. Least
-- privilege per what the pgmq functions the worker calls actually do:
--   jobs_issue: the same rights the worker has on its v1 queue (jobs):
--   read / set_vt / archive touch q_ (select, update, delete) and a_
--   (select, insert);
--   send (dead-letter) -> insert on q_, select on msg_id only (INSERT ...
--   RETURNING msg_id) and usage on its msg_id sequence; the worker still
--   cannot read dead-lettered message bodies.

grant select, update, delete on pgmq.q_jobs_issue to tierbound_worker;
grant select, insert on pgmq.a_jobs_issue to tierbound_worker;

grant insert on pgmq.q_jobs_dead to tierbound_worker;
grant select (msg_id) on pgmq.q_jobs_dead to tierbound_worker;
grant usage on sequence pgmq.q_jobs_dead_msg_id_seq to tierbound_worker;

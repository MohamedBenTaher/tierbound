-- Local dev credentials for the tierbound roles (plan step 1.2 connection
-- mapping). The password is local-only and committed on purpose: it is the
-- same category of secret as the local demo JWT secret printed by
-- `supabase status`. A hosted project sets these passwords from Vault instead
-- and never runs this file.

alter role tierbound_worker login password 'tierbound-local-only';
alter role tierbound_effector login password 'tierbound-local-only';

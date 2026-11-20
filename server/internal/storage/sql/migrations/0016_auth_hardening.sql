-- 0016_auth_hardening.sql
-- Session revocation: tokens issued before this instant (Unix milliseconds)
-- are rejected. "Log out everywhere" and password changes move it forward.
ALTER TABLE users ADD COLUMN sessions_valid_after BIGINT NOT NULL DEFAULT 0;
-- Invites expire. NULL (rows created before this migration) means seven days
-- after created_at.
ALTER TABLE invite ADD COLUMN expires_at TIMESTAMP;

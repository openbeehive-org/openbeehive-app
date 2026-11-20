-- Postgres only (see the file name).
INSERT INTO sync_state (name, val) VALUES ('epoch', floor(random() * 1000000000000000)::bigint + 1);
UPDATE change_log SET logged_at = floor(extract(epoch FROM now()) * 1000)::bigint;
-- Pull reads the log by workspace (idx_change_org_seq); nothing reads it by scope.
DROP INDEX IF EXISTS idx_change_scope_seq;

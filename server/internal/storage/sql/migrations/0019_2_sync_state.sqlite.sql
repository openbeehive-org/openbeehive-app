-- SQLite only (see the file name).
INSERT INTO sync_state (name, val) VALUES ('epoch', abs(random() % 1000000000000000) + 1);
UPDATE change_log SET logged_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
-- Pull reads the log by workspace (idx_change_org_seq); nothing reads it by scope.
DROP INDEX IF EXISTS idx_change_scope_seq;

-- SQLite only (see the file name).
INSERT OR IGNORE INTO sync_state (name, val) VALUES ('horizon', 0);
INSERT OR IGNORE INTO sync_state (name, val) VALUES ('epoch', abs(random() % 1000000000000000) + 1);
UPDATE change_log SET logged_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
-- The daily compaction finds the entries older than its cut-off through this.
CREATE INDEX IF NOT EXISTS idx_change_logged ON change_log (logged_at, seq);
-- Pull reads the log by workspace (idx_change_org_seq); nothing reads it by scope.
DROP INDEX IF EXISTS idx_change_scope_seq;

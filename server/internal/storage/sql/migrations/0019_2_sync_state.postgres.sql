-- Postgres only (see the file name).
INSERT INTO sync_state (name, val) VALUES ('horizon', 0) ON CONFLICT (name) DO NOTHING;
INSERT INTO sync_state (name, val) VALUES ('epoch', floor(random() * 1000000000000000)::bigint + 1)
  ON CONFLICT (name) DO NOTHING;
UPDATE change_log SET logged_at = floor(extract(epoch FROM now()) * 1000)::bigint;
-- The daily compaction finds the entries older than its cut-off through this.
CREATE INDEX IF NOT EXISTS idx_change_logged ON change_log (logged_at, seq);
-- Pull reads the log by workspace (idx_change_org_seq); nothing reads it by scope.
DROP INDEX IF EXISTS idx_change_scope_seq;

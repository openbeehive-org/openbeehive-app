-- 0015_sync_tenant_scope.postgres.sql
-- Postgres only (see the file name). Every statement can run again.
-- Pull is scoped by workspace: it reads the change log by (org_id, seq).
-- org_id was TEXT; it becomes VARCHAR(64) in place, like on the other
-- dialects.
ALTER TABLE change_log
  ALTER COLUMN org_id TYPE VARCHAR(64),
  ALTER COLUMN org_id SET DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_change_org_seq ON change_log (org_id, seq);

-- Apiary sharing never had a writer; the workspace is the only sharing unit.
DROP TABLE IF EXISTS apiary_share;

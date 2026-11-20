-- 0015_sync_tenant_scope.mysql.sql
-- MySQL family only (see the file name). DDL commits at once here, so every
-- statement can run again after a crash part-way through the file.
-- Pull is scoped by workspace: it reads the change log by (org_id, seq).
-- org_id was TEXT, which this family cannot index; it becomes VARCHAR(64) in
-- place.
ALTER TABLE change_log MODIFY org_id VARCHAR(64) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_change_org_seq ON change_log (org_id, seq);

-- Apiary sharing never had a writer; the workspace is the only sharing unit.
DROP TABLE IF EXISTS apiary_share;

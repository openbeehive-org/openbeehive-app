-- 0015_sync_tenant_scope.sqlite.sql
-- SQLite only (see the file name); Postgres and the MySQL family change the
-- column in place in their own 0015 files.
-- Pull is scoped by workspace: it reads the change log by (org_id, seq).
-- org_id was TEXT; it becomes VARCHAR(64), the type the other dialects need
-- to index it. SQLite cannot change a column's type, so the column is
-- rebuilt (add, copy, drop, rename) inside the migration's transaction.
ALTER TABLE change_log ADD COLUMN org_id_new VARCHAR(64) NOT NULL DEFAULT '';
UPDATE change_log SET org_id_new = org_id;
ALTER TABLE change_log DROP COLUMN org_id;
ALTER TABLE change_log RENAME COLUMN org_id_new TO org_id;
CREATE INDEX IF NOT EXISTS idx_change_org_seq ON change_log (org_id, seq);

-- Apiary sharing never had a writer; the workspace is the only sharing unit.
DROP TABLE IF EXISTS apiary_share;

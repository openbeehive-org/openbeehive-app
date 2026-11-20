-- 0015_sync_tenant_scope.sql
-- Pull is scoped by workspace: it reads the change log by (org_id, seq).
-- org_id was TEXT, which MySQL-family databases cannot index, so the column
-- is rebuilt as VARCHAR(64) (add, copy, drop, rename: the only way to change
-- a column type that all three dialects share) and then indexed.
ALTER TABLE change_log ADD COLUMN org_id_new VARCHAR(64) NOT NULL DEFAULT '';
UPDATE change_log SET org_id_new = org_id;
ALTER TABLE change_log DROP COLUMN org_id;
ALTER TABLE change_log RENAME COLUMN org_id_new TO org_id;
CREATE INDEX IF NOT EXISTS idx_change_org_seq ON change_log (org_id, seq);

-- Apiary sharing never had a writer; the workspace is the only sharing unit.
DROP TABLE IF EXISTS apiary_share;

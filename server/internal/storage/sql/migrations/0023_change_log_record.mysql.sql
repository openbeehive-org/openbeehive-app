-- 0023_change_log_record.mysql.sql
-- MySQL family only (see the file name; the other dialects have their own).
--
-- Rewriting the logged changes of one record (erasing a deleted photo, the
-- current hive picture of a 0.4.x app) looks entries up by record. Without an
-- index for that, every such write read the whole change log of the
-- workspace while holding the write lock. Indexed columns must not be TEXT;
-- entity ids are at most 255 characters (the length Push accepts).
ALTER TABLE change_log MODIFY entity    VARCHAR(64)  NOT NULL;
ALTER TABLE change_log MODIFY entity_id VARCHAR(255) NOT NULL;
CREATE INDEX IF NOT EXISTS idx_change_record ON change_log (org_id, entity, entity_id);

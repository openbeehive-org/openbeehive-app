-- 0023_change_log_record.sqlite.sql
-- SQLite only (see the file name; the other dialects have their own).
--
-- Rewriting the logged changes of one record (erasing a deleted photo, the
-- current hive picture of a 0.4.x app) looks entries up by record. Without an
-- index for that, every such write read the whole change log of the
-- workspace while holding the write lock. SQLite does not enforce column
-- types, so the columns stay as they are.
CREATE INDEX IF NOT EXISTS idx_change_record ON change_log (org_id, entity, entity_id);

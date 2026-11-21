-- 0023_change_log_record.postgres.sql
-- Postgres only (see the file name; the other dialects have their own).
--
-- Rewriting the logged changes of one record (erasing a deleted photo, the
-- current hive picture of a 0.4.x app) looks entries up by record. Without an
-- index for that, every such write read the whole change log of the
-- workspace while holding the write lock. Indexed columns are VARCHAR, as
-- everywhere else; entity ids are at most 255 characters (the length Push
-- accepts).
ALTER TABLE change_log ALTER COLUMN entity TYPE VARCHAR(64);
ALTER TABLE change_log ALTER COLUMN entity_id TYPE VARCHAR(255);
CREATE INDEX IF NOT EXISTS idx_change_record ON change_log (org_id, entity, entity_id);

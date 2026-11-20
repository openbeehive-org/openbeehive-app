-- 0018_column_types.mysql.sql
-- MySQL family only (see the file name).

-- TEXT holds 64 KB there, while the same type is unbounded on SQLite and
-- Postgres. Columns that carry inline images or whole-row snapshots need the
-- large variant, otherwise a photo fails with "Data too long".
ALTER TABLE change_log MODIFY payload    LONGTEXT NOT NULL DEFAULT '';
ALTER TABLE inspection MODIFY photo_keys LONGTEXT NOT NULL DEFAULT '';
ALTER TABLE hive       MODIFY photo      LONGTEXT NOT NULL DEFAULT '';

-- 0013 first declared user_passkey.user_id as TEXT. MariaDB took that and
-- indexed a 768-character prefix; give those installs the declared type, so
-- the index covers the whole column as it does on a fresh install.
ALTER TABLE user_passkey MODIFY user_id VARCHAR(64) NOT NULL;

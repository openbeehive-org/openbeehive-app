-- 0018_double_precision.postgres.sql
-- Postgres only (see the file name): REAL is a 4-byte float there, good for
-- about seven digits, while SQLite and MariaDB keep 8-byte doubles under the
-- same name. A weight entered as 12.3 kg read back as 12.300000190734863 and
-- coordinates lost precision. New columns are created as DOUBLE PRECISION by
-- Store.translate; this converts the ones that already exist. Going through
-- text restores the number as it was entered.
ALTER TABLE apiary
  ALTER COLUMN lat TYPE DOUBLE PRECISION USING lat::text::double precision,
  ALTER COLUMN lng TYPE DOUBLE PRECISION USING lng::text::double precision;

ALTER TABLE inspection
  ALTER COLUMN honey_kg         TYPE DOUBLE PRECISION USING honey_kg::text::double precision,
  ALTER COLUMN fed_kg           TYPE DOUBLE PRECISION USING fed_kg::text::double precision,
  ALTER COLUMN weight_kg        TYPE DOUBLE PRECISION USING weight_kg::text::double precision,
  ALTER COLUMN temp_hive        TYPE DOUBLE PRECISION USING temp_hive::text::double precision,
  ALTER COLUMN temp_outside     TYPE DOUBLE PRECISION USING temp_outside::text::double precision,
  ALTER COLUMN humidity_hive    TYPE DOUBLE PRECISION USING humidity_hive::text::double precision,
  ALTER COLUMN humidity_outside TYPE DOUBLE PRECISION USING humidity_outside::text::double precision;

ALTER TABLE harvest
  ALTER COLUMN amount_kg     TYPE DOUBLE PRECISION USING amount_kg::text::double precision,
  ALTER COLUMN water_content TYPE DOUBLE PRECISION USING water_content::text::double precision;

ALTER TABLE event
  ALTER COLUMN amount_kg TYPE DOUBLE PRECISION USING amount_kg::text::double precision;

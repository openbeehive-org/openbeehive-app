-- 0017_record_semantics.postgres.sql
-- Postgres only (see the file name). The table is changed in place and every
-- statement can run again.
-- Sensor readings, corrections of saved records, and "not recorded" values.

-- Time of the last correction of a treatment or harvest; NULL = never edited.
ALTER TABLE treatment ADD COLUMN IF NOT EXISTS amended_at TIMESTAMP;
ALTER TABLE harvest   ADD COLUMN IF NOT EXISTS amended_at TIMESTAMP;

-- The optional numbers of an inspection become nullable: NULL means "not
-- recorded", 0 an observed zero (the climate columns already are). Stored
-- values are kept as they are.
ALTER TABLE inspection
  ALTER COLUMN temperament    DROP NOT NULL, ALTER COLUMN temperament    DROP DEFAULT,
  ALTER COLUMN frames         DROP NOT NULL, ALTER COLUMN frames         DROP DEFAULT,
  ALTER COLUMN stores         DROP NOT NULL, ALTER COLUMN stores         DROP DEFAULT,
  ALTER COLUMN queen_cells    DROP NOT NULL, ALTER COLUMN queen_cells    DROP DEFAULT,
  ALTER COLUMN honey_kg       DROP NOT NULL, ALTER COLUMN honey_kg       DROP DEFAULT,
  ALTER COLUMN brood_frames   DROP NOT NULL, ALTER COLUMN brood_frames   DROP DEFAULT,
  ALTER COLUMN calmness       DROP NOT NULL, ALTER COLUMN calmness       DROP DEFAULT,
  ALTER COLUMN fed_kg         DROP NOT NULL, ALTER COLUMN fed_kg         DROP DEFAULT,
  ALTER COLUMN frames_added   DROP NOT NULL, ALTER COLUMN frames_added   DROP DEFAULT,
  ALTER COLUMN frames_removed DROP NOT NULL, ALTER COLUMN frames_removed DROP DEFAULT,
  ALTER COLUMN weight_kg      DROP NOT NULL, ALTER COLUMN weight_kg      DROP DEFAULT,
  ALTER COLUMN youngest_larva DROP NOT NULL, ALTER COLUMN youngest_larva DROP DEFAULT;

-- Who recorded the inspection (the InspectionSource enum): 0 unspecified
-- (treated as manual), 1 manual, 2 sensor.
ALTER TABLE inspection ADD COLUMN IF NOT EXISTS source INTEGER NOT NULL DEFAULT 0;

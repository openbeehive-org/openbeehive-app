-- 0017_record_semantics.mysql.sql
-- MySQL family only (see the file name). DDL commits at once here, so every
-- statement changes the table in place and can run again after a crash
-- part-way through the file (the runner skips "duplicate column").
-- Sensor readings, corrections of saved records, and "not recorded" values.

-- Time of the last correction of a treatment or harvest; NULL = never edited.
ALTER TABLE treatment ADD COLUMN amended_at DATETIME(6);
ALTER TABLE harvest   ADD COLUMN amended_at DATETIME(6);

-- The optional numbers of an inspection become nullable: NULL means "not
-- recorded", 0 an observed zero (the climate columns already are). Stored
-- values are kept as they are.
ALTER TABLE inspection
  MODIFY temperament    INTEGER NULL DEFAULT NULL,
  MODIFY frames         INTEGER NULL DEFAULT NULL,
  MODIFY stores         INTEGER NULL DEFAULT NULL,
  MODIFY queen_cells    INTEGER NULL DEFAULT NULL,
  MODIFY honey_kg       DOUBLE  NULL DEFAULT NULL,
  MODIFY brood_frames   INTEGER NULL DEFAULT NULL,
  MODIFY calmness       INTEGER NULL DEFAULT NULL,
  MODIFY fed_kg         DOUBLE  NULL DEFAULT NULL,
  MODIFY frames_added   INTEGER NULL DEFAULT NULL,
  MODIFY frames_removed INTEGER NULL DEFAULT NULL,
  MODIFY weight_kg      DOUBLE  NULL DEFAULT NULL,
  MODIFY youngest_larva INTEGER NULL DEFAULT NULL;

-- Who recorded the inspection (the InspectionSource enum): 0 unspecified
-- (treated as manual), 1 manual, 2 sensor.
ALTER TABLE inspection ADD COLUMN source INTEGER NOT NULL DEFAULT 0;

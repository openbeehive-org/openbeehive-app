-- 0017_record_semantics.sql
-- Sensor readings, corrections of saved records, and "not recorded" values.

-- Time of the last correction of a treatment or harvest; NULL = never edited.
ALTER TABLE treatment ADD COLUMN amended_at TIMESTAMP;
ALTER TABLE harvest   ADD COLUMN amended_at TIMESTAMP;

-- The optional numbers of an inspection become nullable: NULL means "not
-- recorded", 0 an observed zero (the climate columns already are). The three
-- dialects share no statement that drops NOT NULL (SQLite has none at all),
-- so the table is rebuilt: new table, copy, drop, rename. Stored values are
-- kept as they are.
--
-- The new table also gains source, who recorded the inspection (the
-- InspectionSource enum): 0 unspecified (treated as manual), 1 manual,
-- 2 sensor.
--
-- The copy names every column: a column that an earlier migration adds to
-- inspection must be added to the three lists below, or it is lost here.
CREATE TABLE inspection_v17 (
  id               TEXT PRIMARY KEY,
  organization_id  VARCHAR(64) NOT NULL,
  hive_id          VARCHAR(64) NOT NULL,
  date             TIMESTAMP NOT NULL,
  weather          TEXT NOT NULL DEFAULT '',
  queen_seen       BOOLEAN NOT NULL DEFAULT FALSE,
  eggs_seen        BOOLEAN NOT NULL DEFAULT FALSE,
  temperament      INTEGER,
  frames           INTEGER,
  stores           INTEGER,
  queen_cells      INTEGER,
  varroa           TEXT NOT NULL DEFAULT '',
  honey_kg         REAL,
  note             TEXT NOT NULL DEFAULT '',
  photo_keys       TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMP NOT NULL,
  updated_hlc      TEXT NOT NULL DEFAULT '',
  deleted          BOOLEAN NOT NULL DEFAULT FALSE,
  field_hlc        TEXT NOT NULL DEFAULT '{}',
  brood_frames     INTEGER,
  calmness         INTEGER,
  fed_kg           REAL,
  frames_added     INTEGER,
  frames_removed   INTEGER,
  drone_frame_cut  BOOLEAN NOT NULL DEFAULT FALSE,
  super_added      BOOLEAN NOT NULL DEFAULT FALSE,
  weight_kg        REAL,
  youngest_larva   INTEGER,
  covered_larva    BOOLEAN NOT NULL DEFAULT FALSE,
  temp_hive        REAL,
  temp_outside     REAL,
  humidity_hive    REAL,
  humidity_outside REAL,
  source           INTEGER NOT NULL DEFAULT 0
);

INSERT INTO inspection_v17 (
  id, organization_id, hive_id, date, weather, queen_seen, eggs_seen, temperament, frames, stores,
  queen_cells, varroa, honey_kg, note, photo_keys, created_at, updated_hlc, deleted, field_hlc,
  brood_frames, calmness, fed_kg, frames_added, frames_removed, drone_frame_cut, super_added,
  weight_kg, youngest_larva, covered_larva, temp_hive, temp_outside, humidity_hive, humidity_outside)
SELECT
  id, organization_id, hive_id, date, weather, queen_seen, eggs_seen, temperament, frames, stores,
  queen_cells, varroa, honey_kg, note, photo_keys, created_at, updated_hlc, deleted, field_hlc,
  brood_frames, calmness, fed_kg, frames_added, frames_removed, drone_frame_cut, super_added,
  weight_kg, youngest_larva, covered_larva, temp_hive, temp_outside, humidity_hive, humidity_outside
FROM inspection;

DROP TABLE inspection;
ALTER TABLE inspection_v17 RENAME TO inspection;
CREATE INDEX IF NOT EXISTS idx_inspection_hive ON inspection (hive_id);

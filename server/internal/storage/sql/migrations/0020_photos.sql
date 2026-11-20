-- 0020_photos.sql
-- Photos as records of their own. A row syncs like every other record and
-- carries a small inline thumbnail; the full-size image lives in the blob
-- store and is reached through the authenticated /photos/ endpoint.
--
-- owner_entity / owner_id name the record the photo belongs to: an
-- inspection, or a hive (its picture).
--
-- The last four columns are the server's own and never sync:
--   blob_key, blob_type, blob_size  where the full image is stored, with the
--                                   type and size found when it was received;
--                                   empty until a device has uploaded it
--   legacy_key                      for a photo converted from the inline
--                                   format of 0.4.x apps: SHA-256 of the data
--                                   URL it was made from
-- thumb comes last so that listing rows does not have to read past it.
CREATE TABLE IF NOT EXISTS photo (
  id              TEXT PRIMARY KEY,
  organization_id VARCHAR(64) NOT NULL,
  owner_entity    VARCHAR(32) NOT NULL DEFAULT '',
  owner_id        VARCHAR(255) NOT NULL DEFAULT '',
  content_type    VARCHAR(64) NOT NULL DEFAULT '',
  size_bytes      BIGINT NOT NULL DEFAULT 0,
  created_at      TIMESTAMP,
  deleted         BOOLEAN NOT NULL DEFAULT FALSE,
  field_hlc       TEXT NOT NULL DEFAULT '{}',
  blob_key        VARCHAR(255) NOT NULL DEFAULT '',
  blob_type       VARCHAR(64) NOT NULL DEFAULT '',
  blob_size       BIGINT NOT NULL DEFAULT 0,
  legacy_key      VARCHAR(64) NOT NULL DEFAULT '',
  thumb           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_photo_org ON photo (organization_id);
CREATE INDEX IF NOT EXISTS idx_photo_owner ON photo (owner_id);
CREATE INDEX IF NOT EXISTS idx_photo_blob ON photo (blob_key);

-- One-time data conversions that run at start-up rather than as SQL (they
-- read and write the blob store). A row means the step has completed.
CREATE TABLE IF NOT EXISTS photo_migration (
  step    VARCHAR(64) PRIMARY KEY,
  done_at TIMESTAMP NOT NULL
);

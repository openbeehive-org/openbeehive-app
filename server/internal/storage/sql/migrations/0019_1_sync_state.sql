-- 0019_1_sync_state.sql
-- State of the sync protocol, one row per name:
--   epoch    random identity of this database. Cursors carry it, so a device
--            notices when it talks to a different database than before.
--   horizon  sequence number up to which the change log has been compacted;
--            a cursor below it can no longer be served from the log.
-- The rows are written by 0019_2_sync_state.<dialect>.sql (each database
-- spells "random number" and "insert unless it is there" differently).
CREATE TABLE IF NOT EXISTS sync_state (
  name VARCHAR(32) PRIMARY KEY,
  val  BIGINT NOT NULL
);

-- One mark per start of the server: the log position it started at and a
-- random tag. A cursor carries the tag of the last start before its
-- position. After a backup has been restored, the server starts at an
-- earlier position than devices have already read, and their cursors no
-- longer carry the tag that belongs there.
CREATE TABLE IF NOT EXISTS sync_boot (
  head BIGINT PRIMARY KEY,
  tag  BIGINT NOT NULL
);

-- When the server received a change (Unix milliseconds). Compaction goes by
-- this, not by the change's own stamp, which is the time on the device that
-- wrote it. 0019_2 sets it to "now" for the entries that exist already.
ALTER TABLE change_log ADD COLUMN logged_at BIGINT NOT NULL DEFAULT 0;

-- A task without an apiary is personal: only its owner sees it. The owner is
-- the user in whose personal scope the task was written. The column records
-- that on the row itself; for existing tasks it is read from the change log,
-- the only place that knew it, before compaction starts removing entries. It
-- is not part of the synced payload.
ALTER TABLE task ADD COLUMN owner_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE task SET owner_id = COALESCE((
  SELECT MIN(SUBSTR(c.scope_id, 6)) FROM change_log c
  WHERE c.entity = 'task' AND c.entity_id = task.id AND c.scope_id LIKE 'user:%'), '');

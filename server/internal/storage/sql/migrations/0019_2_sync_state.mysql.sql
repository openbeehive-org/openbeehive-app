-- MySQL family only (see the file name).
INSERT INTO sync_state (name, val) VALUES ('epoch', FLOOR(RAND() * 1000000000000000) + 1);
-- Seconds since 1970 in UTC, spelled without the function whose name the
-- migration runner would rewrite as a column type.
UPDATE change_log SET logged_at = (TO_SECONDS(UTC_DATE()) - 62167219200 + TIME_TO_SEC(UTC_TIME())) * 1000;
-- Pull reads the log by workspace (idx_change_org_seq); nothing reads it by scope.
DROP INDEX idx_change_scope_seq ON change_log;

-- 0024_session_revocation.sql
-- Sessions ended by signing out. A session token is signed, not stored, so it
-- stays valid until it expires unless the server remembers that it was given
-- up. Only the SHA-256 hash of the token is kept, with the moment the token
-- expires (Unix milliseconds): from then on it is refused anyway and the row
-- is deleted. The server reads the table once at start and keeps it in
-- memory; requests do not query it.
--
-- Both statements can run again, so a start on the MySQL family that stopped
-- part-way through the file (DDL commits at once there) finishes it.
CREATE TABLE IF NOT EXISTS session_revocation (
  token_hash VARCHAR(64) PRIMARY KEY,
  expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_revocation_expires ON session_revocation (expires_at);

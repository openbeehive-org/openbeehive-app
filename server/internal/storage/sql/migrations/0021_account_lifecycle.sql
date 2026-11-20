-- 0021_account_lifecycle.sql
-- One account per email address, whatever its capitalisation. email_key holds
-- the address in its normal form (trimmed, lower case) and is unique. Every
-- account written from now on carries it. Accounts that existed before get it
-- at the first start (Store.Migrate); where two of them differ only by case,
-- one keeps the key and the other keeps NULL until an operator decides, which
-- a unique index allows on every backend.
ALTER TABLE users ADD COLUMN email_key VARCHAR(255);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_key ON users (email_key);

-- Password reset links. Only the SHA-256 hash of the token is stored; a row
-- is deleted when its link is used, and with every other link of the account.
CREATE TABLE IF NOT EXISTS password_reset (
  id         TEXT PRIMARY KEY,
  user_id    VARCHAR(64) NOT NULL,
  token_hash VARCHAR(64) NOT NULL UNIQUE,
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_password_reset_user ON password_reset (user_id);

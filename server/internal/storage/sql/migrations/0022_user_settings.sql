-- 0022_user_settings.sql
-- Preferences that follow an account to all its devices and workspaces: the
-- units weights and temperatures are shown and entered in, and the language.
-- One row per account; its id is the account's id. Records stay metric
-- whatever is chosen here. An empty or NULL value means "not chosen": the
-- device then uses its own default.
CREATE TABLE IF NOT EXISTS user_settings (
  id          TEXT PRIMARY KEY,
  weight_unit VARCHAR(16),
  temp_unit   VARCHAR(16),
  language    VARCHAR(16),
  field_hlc   TEXT NOT NULL DEFAULT '{}',
  deleted     BOOLEAN NOT NULL DEFAULT FALSE
);

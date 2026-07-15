-- Single-user authentication expand migration (compatibility phase A)
-- PostgreSQL only. Safe to re-run. This migration never chooses a keep user.
BEGIN;

SELECT pg_advisory_xact_lock(hashtext('ehome_single_user_auth_expand_v1'));

ALTER TABLE users ADD COLUMN IF NOT EXISTS subject_key varchar(32);
ALTER TABLE users ADD COLUMN IF NOT EXISTS retired_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS session_version bigint NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS initialized_at timestamptz;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_users_subject_key') THEN
    ALTER TABLE users ADD CONSTRAINT chk_users_subject_key
      CHECK (subject_key IS NULL OR subject_key = 'system_admin');
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_users_session_version_positive') THEN
    ALTER TABLE users ADD CONSTRAINT chk_users_session_version_positive
      CHECK (session_version > 0);
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_active_subject
  ON users(subject_key)
  WHERE subject_key = 'system_admin' AND retired_at IS NULL;

CREATE TABLE IF NOT EXISTS auth_states (
  key varchar(32) PRIMARY KEY,
  state varchar(32) NOT NULL,
  security_version bigint NOT NULL DEFAULT 1 CHECK (security_version > 0),
  initialized_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chk_auth_state_key CHECK (key = 'system_auth'),
  CONSTRAINT chk_auth_state_value CHECK (state IN ('uninitialized','initialized','migration_required','disabled'))
);

-- Upgrade migration is fail-closed. New installations use the dedicated
-- bootstrap command to create an uninitialized row explicitly.
INSERT INTO auth_states(key, state, security_version)
VALUES ('system_auth', 'migration_required', 1)
ON CONFLICT (key) DO NOTHING;

COMMIT;

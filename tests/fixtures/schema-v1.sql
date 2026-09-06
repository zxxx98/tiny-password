-- tests/fixtures/schema-v1.sql
-- Snapshot of the released V1 baseline schema (identical in content to
-- migrations/0001_initial.sql at the V1 cutoff). The upgrade drills (T26)
-- apply this by hand, stamp schema_migrations at version 1, and then run
-- the CURRENT binary's migration set on top — proving that an older
-- instance's data survives an upgrade and that a verified pre-upgrade
-- snapshot is taken first. Keep this file in sync with 0001 whenever the
-- baseline moves forward deliberately.

-- Tiny Password initial schema (design §9).
-- All timestamps are RFC 3339 UTC strings; the application supplies them.
-- Foreign keys are enforced per connection (see internal/platform/sqlite).

CREATE TABLE system_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
) WITHOUT ROWID;

-- The schema version lives in schema_migrations (managed by the runner).
INSERT INTO system_state (key, value, updated_at)
VALUES ('initialized', '0', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    username_norm        TEXT NOT NULL UNIQUE,
    username_display     TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    status               TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    must_change_password INTEGER NOT NULL DEFAULT 1 CHECK (must_change_password IN (0, 1)),
    password_hash        TEXT NOT NULL,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE sessions (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at         TEXT NOT NULL,
    absolute_expires_at TEXT NOT NULL,
    idle_expires_at    TEXT NOT NULL,
    revoked_at         TEXT
);

CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_absolute_expiry ON sessions (absolute_expires_at);
CREATE INDEX idx_sessions_idle_expiry ON sessions (idle_expires_at);

-- Short-term rate-limit aggregation only. username_norm here is the parsed
-- normalization key used for throttling; audit events never store usernames.
CREATE TABLE login_attempts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username_norm TEXT,
    source_hash   TEXT NOT NULL,
    outcome       TEXT NOT NULL CHECK (outcome IN ('success', 'failure')),
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_login_attempts_created ON login_attempts (created_at);

CREATE TABLE vault_items (
    id                TEXT PRIMARY KEY,
    vault_scope       TEXT NOT NULL CHECK (vault_scope IN ('personal', 'shared')),
    owner_user_id     TEXT REFERENCES users (id) ON DELETE CASCADE,
    created_by_user_id TEXT REFERENCES users (id) ON DELETE CASCADE,
    item_type         TEXT NOT NULL CHECK (item_type IN ('login', 'ssh_key', 'credit_card', 'identity', 'secure_note')),
    favorite          INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1)),
    payload_version   INTEGER NOT NULL CHECK (payload_version >= 1),
    nonce             BLOB NOT NULL,
    ciphertext        BLOB NOT NULL,
    revision          INTEGER NOT NULL CHECK (revision >= 1),
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    deleted_at        TEXT,
    CHECK (
        (vault_scope = 'personal' AND owner_user_id IS NOT NULL AND created_by_user_id IS NULL)
        OR
        (vault_scope = 'shared' AND owner_user_id IS NULL AND created_by_user_id IS NOT NULL)
    )
);

CREATE INDEX idx_vault_items_owner ON vault_items (owner_user_id) WHERE vault_scope = 'personal';
CREATE INDEX idx_vault_items_creator ON vault_items (created_by_user_id) WHERE vault_scope = 'shared';
CREATE INDEX idx_vault_items_updated ON vault_items (updated_at);
CREATE INDEX idx_vault_items_deleted ON vault_items (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE item_versions (
    item_id         TEXT NOT NULL REFERENCES vault_items (id) ON DELETE CASCADE,
    revision        INTEGER NOT NULL CHECK (revision >= 1),
    payload_version INTEGER NOT NULL CHECK (payload_version >= 1),
    nonce           BLOB NOT NULL,
    ciphertext      BLOB NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (item_id, revision)
);

-- actor_id deliberately has no foreign key: audit events must survive user
-- deletion and keep the opaque internal ID (design §5.3, §7.3).
CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    event       TEXT NOT NULL,
    actor_id    TEXT,
    target_type TEXT,
    target_id   TEXT,
    result      TEXT NOT NULL CHECK (result IN ('success', 'failure')),
    request_id  TEXT,
    created_at  TEXT NOT NULL
);

CREATE INDEX idx_audit_created ON audit_events (created_at);
CREATE INDEX idx_audit_actor ON audit_events (actor_id);

CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE backup_jobs (
    id                TEXT PRIMARY KEY,
    target            TEXT NOT NULL UNIQUE CHECK (target IN ('local', 'r2')),
    enabled           INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    schedule_time     TEXT CHECK (schedule_time IS NULL OR schedule_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    schedule_timezone TEXT,
    retention_daily   INTEGER NOT NULL DEFAULT 7 CHECK (retention_daily >= 0),
    retention_weekly  INTEGER NOT NULL DEFAULT 4 CHECK (retention_weekly >= 0),
    retention_monthly INTEGER NOT NULL DEFAULT 6 CHECK (retention_monthly >= 0),
    config_json       TEXT NOT NULL DEFAULT '{}',
    updated_at        TEXT NOT NULL
);

-- triggered_by (not "trigger": reserved word).
CREATE TABLE backup_runs (
    id          TEXT PRIMARY KEY,
    target      TEXT NOT NULL CHECK (target IN ('local', 'r2')),
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'interrupted')),
    triggered_by TEXT NOT NULL CHECK (triggered_by IN ('manual', 'scheduled')),
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    size_bytes  INTEGER,
    sha256      TEXT,
    error_code  TEXT
);

CREATE INDEX idx_backup_runs_started ON backup_runs (started_at);

-- Idempotency bookkeeping (plan T03): scope + key, keyed HMAC fingerprint,
-- opaque resource ID, expiry. No plaintext payloads are ever stored here.
CREATE TABLE idempotency_keys (
    scope       TEXT NOT NULL,
    key         TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    resource_id TEXT,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed')),
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    PRIMARY KEY (scope, key)
);

CREATE INDEX idx_idempotency_expiry ON idempotency_keys (expires_at);

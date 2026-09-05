-- Public session handles are separate from credential hashes.
ALTER TABLE sessions ADD COLUMN public_id TEXT NOT NULL DEFAULT '';
UPDATE sessions SET public_id = lower(hex(randomblob(16)));
CREATE UNIQUE INDEX idx_sessions_public_id ON sessions(public_id);
ALTER TABLE users ADD COLUMN idle_timeout_minutes INTEGER NOT NULL DEFAULT 15
    CHECK (idle_timeout_minutes BETWEEN 5 AND 30);
CREATE INDEX idx_login_attempts_username_created ON login_attempts(username_norm, created_at);
CREATE INDEX idx_login_attempts_source_created ON login_attempts(source_hash, created_at);

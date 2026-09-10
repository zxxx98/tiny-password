-- Add the secret item type without re-encrypting existing payloads.
-- Both tables are rebuilt so item_versions continues to reference the live
-- vault_items table while foreign-key enforcement remains enabled.
ALTER TABLE item_versions RENAME TO item_versions_old;
ALTER TABLE vault_items RENAME TO vault_items_old;

DROP INDEX idx_vault_items_owner;
DROP INDEX idx_vault_items_creator;
DROP INDEX idx_vault_items_updated;
DROP INDEX idx_vault_items_deleted;

CREATE TABLE vault_items (
    id                TEXT PRIMARY KEY,
    vault_scope       TEXT NOT NULL CHECK (vault_scope IN ('personal', 'shared')),
    owner_user_id     TEXT REFERENCES users (id) ON DELETE CASCADE,
    created_by_user_id TEXT REFERENCES users (id) ON DELETE CASCADE,
    item_type         TEXT NOT NULL CHECK (item_type IN ('login', 'ssh_key', 'credit_card', 'identity', 'secure_note', 'secret')),
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

INSERT INTO vault_items (
    id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite,
    payload_version, nonce, ciphertext, revision, created_at, updated_at, deleted_at
)
SELECT
    id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite,
    payload_version, nonce, ciphertext, revision, created_at, updated_at, deleted_at
FROM vault_items_old;

CREATE TABLE item_versions (
    item_id         TEXT NOT NULL REFERENCES vault_items (id) ON DELETE CASCADE,
    revision        INTEGER NOT NULL CHECK (revision >= 1),
    payload_version INTEGER NOT NULL CHECK (payload_version >= 1),
    nonce           BLOB NOT NULL,
    ciphertext      BLOB NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (item_id, revision)
);

INSERT INTO item_versions (item_id, revision, payload_version, nonce, ciphertext, created_at)
SELECT item_id, revision, payload_version, nonce, ciphertext, created_at
FROM item_versions_old;

DROP TABLE item_versions_old;
DROP TABLE vault_items_old;

CREATE INDEX idx_vault_items_owner ON vault_items (owner_user_id) WHERE vault_scope = 'personal';
CREATE INDEX idx_vault_items_creator ON vault_items (created_by_user_id) WHERE vault_scope = 'shared';
CREATE INDEX idx_vault_items_updated ON vault_items (updated_at);
CREATE INDEX idx_vault_items_deleted ON vault_items (deleted_at) WHERE deleted_at IS NOT NULL;

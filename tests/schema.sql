-- Schéma de test : les tables et colonnes de garmin-mcp que cette interface lit.
--
-- Ce n'est pas une copie des migrations amont (0001_initial.sql,
-- 0002_oauth_contract.sql de tamcore/garmin-mcp) mais le sous-ensemble dont
-- dépendent les requêtes de app/queries.py, avec les mêmes noms de tables, de
-- colonnes et les mêmes conventions : horodatages TEXT en RFC 3339 UTC,
-- identifiants TEXT. Si une requête référence une colonne absente ici, un test
-- échoue — c'est le garde-fou contre une dérive de schéma.

CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE principals (
    id                     TEXT PRIMARY KEY,
    email_normalized       TEXT UNIQUE,
    garmin_account_hash    TEXT UNIQUE,
    garmin_identity_sealed BLOB,
    key_version            INTEGER NOT NULL CHECK (key_version > 0),
    created_at             TEXT    NOT NULL,
    updated_at             TEXT    NOT NULL
);

CREATE TABLE garmin_token_sets (
    principal_id  TEXT PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    record_schema INTEGER NOT NULL CHECK (record_schema > 0),
    version       INTEGER NOT NULL CHECK (version > 0),
    key_version   INTEGER NOT NULL CHECK (key_version > 0),
    sealed        BLOB    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE TABLE oauth_clients (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    secret_hash TEXT,
    is_public   INTEGER NOT NULL CHECK (is_public IN (0, 1)),
    created_at  TEXT NOT NULL,
    disabled_at TEXT
);

CREATE TABLE consents (
    principal_id TEXT NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    client_id    TEXT NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    redirect_uri TEXT NOT NULL,
    resource     TEXT NOT NULL,
    scopes       TEXT NOT NULL,
    granted_at   TEXT NOT NULL,
    revoked_at   TEXT,
    PRIMARY KEY (principal_id, client_id, redirect_uri, resource)
);

CREATE TABLE auth_codes (
    code_hash      TEXT PRIMARY KEY,
    principal_id   TEXT NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    client_id      TEXT NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    scopes         TEXT NOT NULL,
    audience       TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    consumed_at    TEXT
);

CREATE TABLE token_families (
    id                TEXT PRIMARY KEY,
    principal_id      TEXT NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    client_id         TEXT NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    created_at        TEXT NOT NULL,
    revoked_at        TEXT,
    revocation_reason TEXT,
    resource          TEXT NOT NULL DEFAULT ''
);

CREATE TABLE mcp_tokens (
    token_hash  TEXT PRIMARY KEY,
    family_id   TEXT NOT NULL REFERENCES token_families (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
    scopes      TEXT NOT NULL,
    audience    TEXT NOT NULL,
    issued_at   TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    consumed_at TEXT,
    revoked_at  TEXT,
    generation  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE auth_transactions (
    handle_hash           TEXT PRIMARY KEY,
    client_id             TEXT NOT NULL,
    principal_id          TEXT,
    redirect_uri          TEXT NOT NULL,
    scopes                TEXT NOT NULL,
    code_challenge        TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL,
    created_at            TEXT NOT NULL,
    expires_at            TEXT NOT NULL,
    version               INTEGER NOT NULL DEFAULT 0,
    resource              TEXT NOT NULL DEFAULT ''
);

CREATE TABLE audit_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at  TEXT NOT NULL,
    kind         TEXT NOT NULL,
    outcome      TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
    principal_id TEXT,
    client_id    TEXT,
    detail       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE privacy_notice_consents (
    principal_id   TEXT NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    notice_hash    TEXT NOT NULL,
    notice_version TEXT NOT NULL,
    accepted_at    TEXT NOT NULL,
    PRIMARY KEY (principal_id, notice_hash)
);

CREATE INDEX idx_privacy_notice_consents_principal
    ON privacy_notice_consents (principal_id, accepted_at);

CREATE TABLE account_approvals (
    principal_id TEXT PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    state        TEXT NOT NULL CHECK (state IN ('approved', 'blocked')),
    decided_at   TEXT NOT NULL,
    decided_by   TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT ''
);

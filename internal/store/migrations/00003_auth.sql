-- Phase 5: web sessions and API tokens.

-- +goose Up
CREATE TABLE sessions (
    -- SHA-256 of the session cookie value (the raw value is never stored)
    id_hash      TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    remote_addr  TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    -- SHA-256 of the token
    token_hash   TEXT    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL,
    role         TEXT    NOT NULL CHECK (role IN ('admin', 'operator', 'readonly')),
    created_at   INTEGER NOT NULL,
    -- NULL = never expires
    expires_at   INTEGER,
    last_used_at INTEGER
);
CREATE INDEX api_tokens_user ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE sessions;

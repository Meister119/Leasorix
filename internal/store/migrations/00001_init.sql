-- Initial schema.
--
-- Conventions:
--   * IPv4 addresses are stored as INTEGER (uint32, network byte order value)
--     so that range checks, overlap checks and sorting work in SQL.
--   * Timestamps are INTEGER unix seconds unless noted otherwise.
--   * MAC addresses are stored normalised as lower-case "aa:bb:cc:dd:ee:ff".
--   * Client identifiers (option 61) are stored as lower-case hex.

-- +goose Up
CREATE TABLE scopes (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    network     INTEGER NOT NULL,
    prefix_len  INTEGER NOT NULL CHECK (prefix_len BETWEEN 1 AND 32),
    start_ip    INTEGER NOT NULL,
    end_ip      INTEGER NOT NULL,
    -- lease duration in seconds, 0 = unlimited
    lease_time  INTEGER NOT NULL CHECK (lease_time >= 0),
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK (start_ip <= end_ip)
);

CREATE TABLE exclusions (
    id       INTEGER PRIMARY KEY,
    scope_id INTEGER NOT NULL REFERENCES scopes (id) ON DELETE CASCADE,
    start_ip INTEGER NOT NULL,
    end_ip   INTEGER NOT NULL,
    CHECK (start_ip <= end_ip)
);
CREATE INDEX exclusions_scope ON exclusions (scope_id);

CREATE TABLE reservations (
    id          INTEGER PRIMARY KEY,
    scope_id    INTEGER NOT NULL REFERENCES scopes (id) ON DELETE CASCADE,
    ip          INTEGER NOT NULL UNIQUE,
    mac         TEXT    NOT NULL DEFAULT '',
    client_id   TEXT    NOT NULL DEFAULT '',
    hostname    TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK (mac <> '' OR client_id <> '')
);
CREATE INDEX reservations_scope ON reservations (scope_id);
CREATE UNIQUE INDEX reservations_mac ON reservations (scope_id, mac) WHERE mac <> '';
CREATE UNIQUE INDEX reservations_client_id ON reservations (scope_id, client_id) WHERE client_id <> '';

-- Option values. Exactly one owner level applies:
--   server:      scope_id IS NULL AND reservation_id IS NULL
--   scope:       scope_id set
--   reservation: reservation_id set
CREATE TABLE options (
    id             INTEGER PRIMARY KEY,
    scope_id       INTEGER REFERENCES scopes (id) ON DELETE CASCADE,
    reservation_id INTEGER REFERENCES reservations (id) ON DELETE CASCADE,
    code           INTEGER NOT NULL CHECK (code BETWEEN 1 AND 254),
    -- value type, e.g. ip, ip-list, string, uint8, uint16, uint32, bool, hex
    type           TEXT    NOT NULL,
    -- canonical textual representation of the value
    value          TEXT    NOT NULL,
    CHECK (scope_id IS NULL OR reservation_id IS NULL)
);
CREATE UNIQUE INDEX options_owner_code ON options (IFNULL(scope_id, 0), IFNULL(reservation_id, 0), code);

CREATE TABLE leases (
    ip         INTEGER PRIMARY KEY,
    scope_id   INTEGER NOT NULL REFERENCES scopes (id) ON DELETE CASCADE,
    mac        TEXT    NOT NULL DEFAULT '',
    client_id  TEXT    NOT NULL DEFAULT '',
    hostname   TEXT    NOT NULL DEFAULT '',
    -- active | expired | released | declined
    state      TEXT    NOT NULL,
    starts_at  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX leases_scope ON leases (scope_id);
CREATE INDEX leases_mac ON leases (mac);
CREATE INDEX leases_client_id ON leases (client_id) WHERE client_id <> '';
CREATE INDEX leases_state_expires ON leases (state, expires_at);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY,
    -- unix milliseconds
    ts          INTEGER NOT NULL,
    actor       TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    object_type TEXT    NOT NULL,
    object_id   TEXT    NOT NULL DEFAULT '',
    -- JSON object with action specific details
    details     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_ts ON audit_log (ts);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin', 'operator', 'readonly')),
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- +goose Down
DROP TABLE users;
DROP TABLE audit_log;
DROP TABLE leases;
DROP TABLE options;
DROP TABLE reservations;
DROP TABLE exclusions;
DROP TABLE scopes;

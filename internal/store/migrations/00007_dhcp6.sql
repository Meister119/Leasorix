-- Phase 10: DHCPv6 (stateful, IA_NA, prefix delegation).
--
-- IPv6 has its own tables; the IPv4 tables and code stay untouched.
-- Addresses and prefixes are stored in their canonical text form (as
-- produced by Go's netip), containment checks are done in Go.
--
--   * scopes6: a link prefix with an optional address range (IA_NA) and an
--     optional delegation pool (IA_PD).
--   * reservations6: fixed address for a DUID (optionally a single IAID).
--   * options6: option values (16-bit codes) of the server, a scope or a
--     reservation.
--   * leases6: bindings of addresses (kind na, prefix_len 128) and delegated
--     prefixes (kind pd). Local state of this server, not replicated.
--   * local_state: per-server values such as the DHCPv6 server DUID.

-- +goose Up
CREATE TABLE scopes6 (
    id                 INTEGER PRIMARY KEY,
    name               TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description        TEXT    NOT NULL DEFAULT '',
    prefix             TEXT    NOT NULL UNIQUE,
    start_ip           TEXT    NOT NULL DEFAULT '',
    end_ip             TEXT    NOT NULL DEFAULT '',
    preferred_lifetime INTEGER NOT NULL,
    valid_lifetime     INTEGER NOT NULL,
    rapid_commit       INTEGER NOT NULL DEFAULT 0,
    preference         INTEGER NOT NULL DEFAULT 0,
    enabled            INTEGER NOT NULL DEFAULT 1,
    pd_pool            TEXT    NOT NULL DEFAULT '',
    pd_length          INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE exclusions6 (
    id       INTEGER PRIMARY KEY,
    scope_id INTEGER NOT NULL REFERENCES scopes6 (id) ON DELETE CASCADE,
    start_ip TEXT    NOT NULL,
    end_ip   TEXT    NOT NULL
);
CREATE INDEX exclusions6_scope ON exclusions6 (scope_id);

CREATE TABLE reservations6 (
    id          INTEGER PRIMARY KEY,
    scope_id    INTEGER NOT NULL REFERENCES scopes6 (id) ON DELETE CASCADE,
    duid        TEXT    NOT NULL,
    iaid        INTEGER NOT NULL DEFAULT -1,
    ip          TEXT    NOT NULL,
    hostname    TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (scope_id, ip),
    UNIQUE (scope_id, duid, iaid)
);

CREATE TABLE options6 (
    id             INTEGER PRIMARY KEY,
    scope_id       INTEGER REFERENCES scopes6 (id) ON DELETE CASCADE,
    reservation_id INTEGER REFERENCES reservations6 (id) ON DELETE CASCADE,
    code           INTEGER NOT NULL CHECK (code BETWEEN 1 AND 65535),
    type           TEXT    NOT NULL,
    value          TEXT    NOT NULL,
    CHECK (scope_id IS NULL OR reservation_id IS NULL)
);
CREATE UNIQUE INDEX options6_owner_code ON options6 (IFNULL(scope_id, 0), IFNULL(reservation_id, 0), code);

CREATE TABLE leases6 (
    ip              TEXT    PRIMARY KEY,
    prefix_len      INTEGER NOT NULL,
    kind            TEXT    NOT NULL CHECK (kind IN ('na', 'pd')),
    scope_id        INTEGER NOT NULL REFERENCES scopes6 (id) ON DELETE CASCADE,
    duid            TEXT    NOT NULL,
    iaid            INTEGER NOT NULL,
    hostname        TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL,
    starts_at       INTEGER NOT NULL,
    preferred_until INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX leases6_client ON leases6 (duid, iaid);
CREATE INDEX leases6_scope ON leases6 (scope_id);

CREATE TABLE local_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes6_insert AFTER INSERT ON scopes6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes6_update AFTER UPDATE ON scopes6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes6_delete AFTER DELETE ON scopes6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions6_insert AFTER INSERT ON exclusions6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions6_update AFTER UPDATE ON exclusions6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions6_delete AFTER DELETE ON exclusions6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations6_insert AFTER INSERT ON reservations6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations6_update AFTER UPDATE ON reservations6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations6_delete AFTER DELETE ON reservations6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options6_insert AFTER INSERT ON options6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options6_update AFTER UPDATE ON options6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options6_delete AFTER DELETE ON options6 BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cfgver_scopes6_insert;
DROP TRIGGER cfgver_scopes6_update;
DROP TRIGGER cfgver_scopes6_delete;
DROP TRIGGER cfgver_exclusions6_insert;
DROP TRIGGER cfgver_exclusions6_update;
DROP TRIGGER cfgver_exclusions6_delete;
DROP TRIGGER cfgver_reservations6_insert;
DROP TRIGGER cfgver_reservations6_update;
DROP TRIGGER cfgver_reservations6_delete;
DROP TRIGGER cfgver_options6_insert;
DROP TRIGGER cfgver_options6_update;
DROP TRIGGER cfgver_options6_delete;
DROP TABLE local_state;
DROP TABLE leases6;
DROP TABLE options6;
DROP TABLE reservations6;
DROP TABLE exclusions6;
DROP TABLE scopes6;

-- Phase 9: dynamic DNS updates (RFC 2136).
--
--   * dns_keys: TSIG keys that sign the updates. The secret is needed in
--     clear text to compute the HMAC; it is never returned by the API.
--   * dns_zones: zones that receive updates, each with its primary server
--     and optionally a key (NULL = unsigned updates).
--   * scopes.dns_*: per scope settings (Windows "DNS" tab).
--   * dns_registrations: what this server wants to have and has registered
--     in DNS per leased address. It is local state of this server (like the
--     HA state) and neither replicated nor part of exports.

-- +goose Up
CREATE TABLE dns_keys (
    id        INTEGER PRIMARY KEY,
    name      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    algorithm TEXT NOT NULL,
    secret    TEXT NOT NULL
);

CREATE TABLE dns_zones (
    id     INTEGER PRIMARY KEY,
    name   TEXT NOT NULL UNIQUE COLLATE NOCASE,
    server TEXT NOT NULL,
    key_id INTEGER REFERENCES dns_keys (id)
);
CREATE INDEX dns_zones_key ON dns_zones (key_id);

ALTER TABLE scopes ADD COLUMN dns_mode TEXT NOT NULL DEFAULT 'never';
ALTER TABLE scopes ADD COLUMN dns_domain TEXT NOT NULL DEFAULT '';
ALTER TABLE scopes ADD COLUMN dns_ttl INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scopes ADD COLUMN dns_cleanup INTEGER NOT NULL DEFAULT 1;
ALTER TABLE scopes ADD COLUMN dns_protect INTEGER NOT NULL DEFAULT 1;

CREATE TABLE dns_registrations (
    ip           INTEGER PRIMARY KEY,
    scope_id     INTEGER NOT NULL DEFAULT 0,
    client       TEXT NOT NULL DEFAULT '',
    cleanup      INTEGER NOT NULL DEFAULT 1,
    ttl          INTEGER NOT NULL DEFAULT 0,
    -- wanted: name, which records, DHCID ('' = no name protection)
    want_fqdn    TEXT NOT NULL DEFAULT '',
    want_forward INTEGER NOT NULL DEFAULT 0,
    want_reverse INTEGER NOT NULL DEFAULT 0,
    want_dhcid   TEXT NOT NULL DEFAULT '',
    -- registered: name of the A record (with its DHCID) and of the PTR
    have_forward TEXT NOT NULL DEFAULT '',
    have_dhcid   TEXT NOT NULL DEFAULT '',
    have_reverse TEXT NOT NULL DEFAULT '',
    conflict     INTEGER NOT NULL DEFAULT 0,
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_attempt INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL DEFAULT 0
);

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_keys_insert AFTER INSERT ON dns_keys BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_keys_update AFTER UPDATE ON dns_keys BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_keys_delete AFTER DELETE ON dns_keys BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_zones_insert AFTER INSERT ON dns_zones BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_zones_update AFTER UPDATE ON dns_zones BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_dns_zones_delete AFTER DELETE ON dns_zones BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cfgver_dns_keys_insert;
DROP TRIGGER cfgver_dns_keys_update;
DROP TRIGGER cfgver_dns_keys_delete;
DROP TRIGGER cfgver_dns_zones_insert;
DROP TRIGGER cfgver_dns_zones_update;
DROP TRIGGER cfgver_dns_zones_delete;
DROP TABLE dns_registrations;
ALTER TABLE scopes DROP COLUMN dns_protect;
ALTER TABLE scopes DROP COLUMN dns_cleanup;
ALTER TABLE scopes DROP COLUMN dns_ttl;
ALTER TABLE scopes DROP COLUMN dns_domain;
ALTER TABLE scopes DROP COLUMN dns_mode;
DROP TABLE dns_zones;
DROP TABLE dns_keys;

-- Phase 3: option values with policy owner, classes, policies, MAC filters,
-- custom option definitions and key/value settings.

-- +goose Up
CREATE TABLE classes (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    -- vendor: matches option 60, user: matches option 77
    type        TEXT NOT NULL CHECK (type IN ('vendor', 'user')),
    -- match pattern, wildcards * and ? allowed
    data        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE policies (
    id               INTEGER PRIMARY KEY,
    -- NULL = server level policy
    scope_id         INTEGER REFERENCES scopes (id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    processing_order INTEGER NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    -- how conditions are combined
    operator         TEXT    NOT NULL CHECK (operator IN ('and', 'or')),
    -- lease duration override in seconds, NULL = inherit, 0 = unlimited
    lease_time       INTEGER CHECK (lease_time IS NULL OR lease_time >= 0),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX policies_name ON policies (IFNULL(scope_id, 0), name);

CREATE TABLE policy_conditions (
    id        INTEGER PRIMARY KEY,
    policy_id INTEGER NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    field     TEXT    NOT NULL,
    operator  TEXT    NOT NULL CHECK (operator IN ('eq', 'ne')),
    value     TEXT    NOT NULL
);
CREATE INDEX policy_conditions_policy ON policy_conditions (policy_id);

-- Address ranges reserved for clients matching a scope level policy.
CREATE TABLE policy_ranges (
    id        INTEGER PRIMARY KEY,
    policy_id INTEGER NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    start_ip  INTEGER NOT NULL,
    end_ip    INTEGER NOT NULL,
    CHECK (start_ip <= end_ip)
);
CREATE INDEX policy_ranges_policy ON policy_ranges (policy_id);

-- User defined option codes (built-in ones live in the code).
CREATE TABLE option_definitions (
    code        INTEGER PRIMARY KEY CHECK (code BETWEEN 1 AND 254),
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    type        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

-- Option values gain a policy owner. SQLite cannot alter CHECK constraints,
-- so the table is rebuilt.
CREATE TABLE options_new (
    id             INTEGER PRIMARY KEY,
    scope_id       INTEGER REFERENCES scopes (id) ON DELETE CASCADE,
    reservation_id INTEGER REFERENCES reservations (id) ON DELETE CASCADE,
    policy_id      INTEGER REFERENCES policies (id) ON DELETE CASCADE,
    code           INTEGER NOT NULL CHECK (code BETWEEN 1 AND 254),
    type           TEXT    NOT NULL,
    value          TEXT    NOT NULL,
    CHECK ((scope_id IS NOT NULL) + (reservation_id IS NOT NULL) + (policy_id IS NOT NULL) <= 1)
);
INSERT INTO options_new (id, scope_id, reservation_id, code, type, value)
    SELECT id, scope_id, reservation_id, code, type, value FROM options;
DROP TABLE options;
ALTER TABLE options_new RENAME TO options;
CREATE UNIQUE INDEX options_owner_code ON options (IFNULL(scope_id, 0), IFNULL(reservation_id, 0), IFNULL(policy_id, 0), code);

CREATE TABLE mac_filters (
    id          INTEGER PRIMARY KEY,
    list        TEXT    NOT NULL CHECK (list IN ('allow', 'deny')),
    -- canonical MAC or wildcard pattern like 00:15:5d:*
    pattern     TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    UNIQUE (list, pattern)
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE mac_filters;
CREATE TABLE options_old (
    id             INTEGER PRIMARY KEY,
    scope_id       INTEGER REFERENCES scopes (id) ON DELETE CASCADE,
    reservation_id INTEGER REFERENCES reservations (id) ON DELETE CASCADE,
    code           INTEGER NOT NULL CHECK (code BETWEEN 1 AND 254),
    type           TEXT    NOT NULL,
    value          TEXT    NOT NULL,
    CHECK (scope_id IS NULL OR reservation_id IS NULL)
);
INSERT INTO options_old (id, scope_id, reservation_id, code, type, value)
    SELECT id, scope_id, reservation_id, code, type, value FROM options WHERE policy_id IS NULL;
DROP TABLE options;
ALTER TABLE options_old RENAME TO options;
CREATE UNIQUE INDEX options_owner_code ON options (IFNULL(scope_id, 0), IFNULL(reservation_id, 0), code);
DROP TABLE option_definitions;
DROP TABLE policy_ranges;
DROP TABLE policy_conditions;
DROP TABLE policies;
DROP TABLE classes;

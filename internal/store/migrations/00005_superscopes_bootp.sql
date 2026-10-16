-- Phase 7: superscopes and BOOTP.
--
--   * A superscope groups scopes that serve the same physical network
--     (multinet): clients of the link may get addresses from every member.
--     Membership is a separate table so that deleting either side cascades
--     cleanly; a scope belongs to at most one superscope.
--   * scopes.bootp enables dynamic BOOTP for a scope (reservations always
--     answer BOOTP clients).

-- +goose Up
CREATE TABLE superscopes (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE superscope_members (
    scope_id      INTEGER PRIMARY KEY REFERENCES scopes (id) ON DELETE CASCADE,
    superscope_id INTEGER NOT NULL REFERENCES superscopes (id) ON DELETE CASCADE
);
CREATE INDEX superscope_members_superscope ON superscope_members (superscope_id);

ALTER TABLE scopes ADD COLUMN bootp INTEGER NOT NULL DEFAULT 0;

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscopes_insert AFTER INSERT ON superscopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscopes_update AFTER UPDATE ON superscopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscopes_delete AFTER DELETE ON superscopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscope_members_insert AFTER INSERT ON superscope_members BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscope_members_update AFTER UPDATE ON superscope_members BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_superscope_members_delete AFTER DELETE ON superscope_members BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cfgver_superscopes_insert;
DROP TRIGGER cfgver_superscopes_update;
DROP TRIGGER cfgver_superscopes_delete;
DROP TRIGGER cfgver_superscope_members_insert;
DROP TRIGGER cfgver_superscope_members_update;
DROP TRIGGER cfgver_superscope_members_delete;
ALTER TABLE scopes DROP COLUMN bootp;
DROP TABLE superscope_members;
DROP TABLE superscopes;

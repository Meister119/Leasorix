-- Phase 6: high availability.
--
--   * leases.updated_at switches to unix milliseconds and leases record the
--     node that made the change (origin) - both are needed to merge lease
--     tables of two servers deterministically.
--   * ha_state keeps the local failover state (never replicated).
--   * config_version counts every change of replicated configuration
--     tables (maintained by triggers); the config leader pushes a new
--     snapshot to its partner whenever it changes.

-- +goose Up
UPDATE leases SET updated_at = updated_at * 1000;
ALTER TABLE leases ADD COLUMN origin TEXT NOT NULL DEFAULT '';

CREATE TABLE ha_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE config_version (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);
INSERT INTO config_version (id, version) VALUES (1, 1);

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes_insert AFTER INSERT ON scopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes_update AFTER UPDATE ON scopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_scopes_delete AFTER DELETE ON scopes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions_insert AFTER INSERT ON exclusions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions_update AFTER UPDATE ON exclusions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_exclusions_delete AFTER DELETE ON exclusions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations_insert AFTER INSERT ON reservations BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations_update AFTER UPDATE ON reservations BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_reservations_delete AFTER DELETE ON reservations BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options_insert AFTER INSERT ON options BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options_update AFTER UPDATE ON options BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_options_delete AFTER DELETE ON options BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_classes_insert AFTER INSERT ON classes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_classes_update AFTER UPDATE ON classes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_classes_delete AFTER DELETE ON classes BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policies_insert AFTER INSERT ON policies BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policies_update AFTER UPDATE ON policies BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policies_delete AFTER DELETE ON policies BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_conditions_insert AFTER INSERT ON policy_conditions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_conditions_update AFTER UPDATE ON policy_conditions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_conditions_delete AFTER DELETE ON policy_conditions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_ranges_insert AFTER INSERT ON policy_ranges BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_ranges_update AFTER UPDATE ON policy_ranges BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_policy_ranges_delete AFTER DELETE ON policy_ranges BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_option_definitions_insert AFTER INSERT ON option_definitions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_option_definitions_update AFTER UPDATE ON option_definitions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_option_definitions_delete AFTER DELETE ON option_definitions BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_mac_filters_insert AFTER INSERT ON mac_filters BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_mac_filters_update AFTER UPDATE ON mac_filters BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_mac_filters_delete AFTER DELETE ON mac_filters BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_settings_insert AFTER INSERT ON settings BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_settings_update AFTER UPDATE ON settings BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_settings_delete AFTER DELETE ON settings BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_users_insert AFTER INSERT ON users BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_users_update AFTER UPDATE OF username, password_hash, role, disabled ON users BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_users_delete AFTER DELETE ON users BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_api_tokens_insert AFTER INSERT ON api_tokens BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_api_tokens_update AFTER UPDATE OF user_id, name, token_hash, prefix, role, expires_at ON api_tokens BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cfgver_api_tokens_delete AFTER DELETE ON api_tokens BEGIN
    UPDATE config_version SET version = version + 1 WHERE id = 1;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cfgver_scopes_insert;
DROP TRIGGER cfgver_scopes_update;
DROP TRIGGER cfgver_scopes_delete;
DROP TRIGGER cfgver_exclusions_insert;
DROP TRIGGER cfgver_exclusions_update;
DROP TRIGGER cfgver_exclusions_delete;
DROP TRIGGER cfgver_reservations_insert;
DROP TRIGGER cfgver_reservations_update;
DROP TRIGGER cfgver_reservations_delete;
DROP TRIGGER cfgver_options_insert;
DROP TRIGGER cfgver_options_update;
DROP TRIGGER cfgver_options_delete;
DROP TRIGGER cfgver_classes_insert;
DROP TRIGGER cfgver_classes_update;
DROP TRIGGER cfgver_classes_delete;
DROP TRIGGER cfgver_policies_insert;
DROP TRIGGER cfgver_policies_update;
DROP TRIGGER cfgver_policies_delete;
DROP TRIGGER cfgver_policy_conditions_insert;
DROP TRIGGER cfgver_policy_conditions_update;
DROP TRIGGER cfgver_policy_conditions_delete;
DROP TRIGGER cfgver_policy_ranges_insert;
DROP TRIGGER cfgver_policy_ranges_update;
DROP TRIGGER cfgver_policy_ranges_delete;
DROP TRIGGER cfgver_option_definitions_insert;
DROP TRIGGER cfgver_option_definitions_update;
DROP TRIGGER cfgver_option_definitions_delete;
DROP TRIGGER cfgver_mac_filters_insert;
DROP TRIGGER cfgver_mac_filters_update;
DROP TRIGGER cfgver_mac_filters_delete;
DROP TRIGGER cfgver_settings_insert;
DROP TRIGGER cfgver_settings_update;
DROP TRIGGER cfgver_settings_delete;
DROP TRIGGER cfgver_users_insert;
DROP TRIGGER cfgver_users_update;
DROP TRIGGER cfgver_users_delete;
DROP TRIGGER cfgver_api_tokens_insert;
DROP TRIGGER cfgver_api_tokens_update;
DROP TRIGGER cfgver_api_tokens_delete;
DROP TABLE config_version;
DROP TABLE ha_state;
ALTER TABLE leases DROP COLUMN origin;
UPDATE leases SET updated_at = updated_at / 1000;

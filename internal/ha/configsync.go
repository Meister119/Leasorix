package ha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/meister119/leasorix/internal/store"
)

// configTables are the replicated configuration tables (see
// store.ConfigTables).
var configTables = store.ConfigTables

type configDump struct {
	Tables []store.TableData `json:"tables"`
}

// configSnapshot is a serialised configuration with its version (from the
// config_version table, maintained by triggers) and content hash.
type configSnapshot struct {
	Version uint64
	Hash    []byte
	Data    []byte
}

func readConfigVersion(ctx context.Context, db *store.DB) (uint64, error) {
	var v int64
	err := db.R.QueryRowContext(ctx, `SELECT version FROM config_version WHERE id = 1`).Scan(&v)
	return uint64(v), err
}

// dumpConfig serialises all configuration tables from one consistent read
// transaction.
func dumpConfig(ctx context.Context, db *store.DB) (*configSnapshot, error) {
	tx, err := db.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM config_version WHERE id = 1`).Scan(&version); err != nil {
		return nil, err
	}
	tables, err := store.DumpTables(ctx, tx, configTables)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(configDump{Tables: tables})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &configSnapshot{Version: uint64(version), Hash: sum[:], Data: data}, nil
}

// applyConfig replaces the local configuration tables with the snapshot of
// the config leader in one transaction. Local data that refers to the
// configuration is adjusted: leases are reassigned to the scope containing
// their address (or removed), sessions of removed users and of users whose
// password, role or disabled flag changed are ended. API token usage
// timestamps are kept.
func applyConfig(ctx context.Context, db *store.DB, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var d configDump
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	var (
		usersBefore map[int64]string
		lastUsed    = map[int64]any{}
	)
	return db.ReplaceTables(ctx, configTables, d.Tables, store.ReplaceOptions{
		Before: func(ctx context.Context, tx *sql.Tx) error {
			var err error
			if usersBefore, err = userFingerprints(ctx, tx); err != nil {
				return err
			}
			return scanPairs(ctx, tx, `SELECT id, last_used_at FROM api_tokens WHERE last_used_at IS NOT NULL`, func(id int64, v any) { lastUsed[id] = v })
		},
		After: func(ctx context.Context, tx *sql.Tx) error {
			for id, v := range lastUsed {
				if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = MAX(IFNULL(last_used_at, 0), ?) WHERE id = ?`, v, id); err != nil {
					return err
				}
			}
			usersAfter, err := userFingerprints(ctx, tx)
			if err != nil {
				return err
			}
			for id, fp := range usersBefore {
				if after, ok := usersAfter[id]; !ok || after != fp {
					if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id NOT IN (SELECT id FROM users)`); err != nil {
				return err
			}
			return store.RemapLeases(ctx, tx)
		},
	})
}

func userFingerprints(ctx context.Context, tx *sql.Tx) (map[int64]string, error) {
	out := map[int64]string{}
	rows, err := tx.QueryContext(ctx, `SELECT id, username || '|' || password_hash || '|' || role || '|' || disabled FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			fp string
		)
		if err := rows.Scan(&id, &fp); err != nil {
			return nil, err
		}
		out[id] = fp
	}
	return out, rows.Err()
}

func scanPairs(ctx context.Context, tx *sql.Tx, q string, fn func(int64, any)) error {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			v  any
		)
		if err := rows.Scan(&id, &v); err != nil {
			return err
		}
		fn(id, v)
	}
	return rows.Err()
}

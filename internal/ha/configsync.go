package ha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/meister119/leasorix/internal/store"
)

// configTables are the replicated configuration tables in dependency order
// (referenced tables first). Everything else (leases, audit log, sessions,
// HA state) is local to each server.
var configTables = []string{
	"scopes", "classes", "option_definitions", "users",
	"exclusions", "reservations", "policies", "policy_conditions", "policy_ranges",
	"options", "mac_filters", "settings", "api_tokens",
}

type tableDump struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type configDump struct {
	Tables []tableDump `json:"tables"`
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
	var d configDump
	for _, t := range configTables {
		td, err := dumpTable(ctx, tx, t)
		if err != nil {
			return nil, fmt.Errorf("dump %s: %w", t, err)
		}
		d.Tables = append(d.Tables, td)
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &configSnapshot{Version: uint64(version), Hash: sum[:], Data: data}, nil
}

func dumpTable(ctx context.Context, tx *sql.Tx, table string) (tableDump, error) {
	td := tableDump{Name: table, Rows: [][]any{}}
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY rowid`) //nolint:gosec // table names come from the fixed configTables list
	if err != nil {
		return td, err
	}
	defer rows.Close()
	if td.Columns, err = rows.Columns(); err != nil {
		return td, err
	}
	for rows.Next() {
		vals := make([]any, len(td.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return td, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		td.Rows = append(td.Rows, vals)
	}
	return td, rows.Err()
}

// applyConfig replaces the local configuration tables with the snapshot of
// the config leader in one transaction. Local data that refers to removed
// configuration is cleaned up: leases of removed scopes or outside their
// scope's subnet, sessions of removed users and of users whose password,
// role or disabled flag changed. API token usage timestamps are kept.
func applyConfig(ctx context.Context, db *store.DB, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var d configDump
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	byName := map[string]*tableDump{}
	for i := range d.Tables {
		byName[d.Tables[i].Name] = &d.Tables[i]
	}
	for _, t := range configTables {
		if byName[t] == nil {
			return fmt.Errorf("configuration snapshot lacks table %s (incompatible versions?)", t)
		}
	}

	conn, err := db.W.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Foreign keys are switched off so that deleting and re-inserting the
	// scopes does not cascade into the (local) lease table. Consistency is
	// verified with foreign_key_check before committing.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	usersBefore, err := userFingerprints(ctx, tx)
	if err != nil {
		return err
	}
	lastUsed := map[int64]any{}
	if err := scanPairs(ctx, tx, `SELECT id, last_used_at FROM api_tokens WHERE last_used_at IS NOT NULL`, func(id int64, v any) { lastUsed[id] = v }); err != nil {
		return err
	}

	for i := len(configTables) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+configTables[i]); err != nil {
			return err
		}
	}
	for _, t := range configTables {
		if err := insertTable(ctx, tx, byName[t]); err != nil {
			return fmt.Errorf("apply %s: %w", t, err)
		}
	}
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
	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id NOT IN (SELECT id FROM users)`,
		`DELETE FROM leases WHERE NOT EXISTS (SELECT 1 FROM scopes s WHERE s.id = leases.scope_id
			AND leases.ip >= s.network AND leases.ip < s.network + (1 << (32 - s.prefix_len)))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTable(ctx context.Context, tx *sql.Tx, td *tableDump) error {
	local, err := tableColumns(ctx, tx, td.Name)
	if err != nil {
		return err
	}
	for _, c := range td.Columns {
		if !local[c] {
			return fmt.Errorf("unknown column %q (the partner runs a different schema version)", c)
		}
	}
	if len(td.Rows) == 0 {
		return nil
	}
	// Table names come from configTables, columns were checked against the
	// local schema above.
	q := `INSERT INTO ` + td.Name + ` (` + strings.Join(td.Columns, ", ") + `) VALUES (?` + strings.Repeat(", ?", len(td.Columns)-1) + `)` //nolint:gosec // see above
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range td.Rows {
		if len(row) != len(td.Columns) {
			return errors.New("malformed row")
		}
		args := make([]any, len(row))
		for i, v := range row {
			args[i], err = sqlValue(v)
			if err != nil {
				return err
			}
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func sqlValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		return x.Float64()
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("unknown table %s", table)
	}
	return out, rows.Err()
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

func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var (
			table, parent string
			rowid, fkid   sql.NullInt64
		)
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		return fmt.Errorf("configuration snapshot violates a foreign key: %s row %d references %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}

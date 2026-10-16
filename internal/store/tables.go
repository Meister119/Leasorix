package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TableData is the content of one table: column names and rows. It is used
// to copy data between databases (HA configuration sync, backup restore,
// configuration import).
type TableData struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Querier is implemented by *sql.DB, *sql.Tx and *sql.Conn.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DumpTables reads the given tables completely (rows in rowid order).
// Table names must be trusted (they are interpolated).
func DumpTables(ctx context.Context, q Querier, tables []string) ([]TableData, error) {
	out := make([]TableData, 0, len(tables))
	for _, t := range tables {
		td, err := dumpTable(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("dump %s: %w", t, err)
		}
		out = append(out, td)
	}
	return out, nil
}

func dumpTable(ctx context.Context, q Querier, table string) (TableData, error) {
	td := TableData{Name: table, Rows: [][]any{}}
	rows, err := q.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY rowid`)
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

// ReplaceOptions hook into ReplaceTables.
type ReplaceOptions struct {
	// Before runs in the transaction before the tables are emptied.
	Before func(ctx context.Context, tx *sql.Tx) error
	// After runs in the transaction after the new rows were inserted and
	// before the foreign key check (cleanup of dependent local data).
	After func(ctx context.Context, tx *sql.Tx) error
}

// ReplaceTables replaces the content of the tables listed in order (which
// must be in dependency order, referenced tables first) with data, in one
// transaction. Foreign keys are disabled meanwhile so that emptying a table
// does not cascade into tables that are not replaced (e.g. leases); the
// result is verified with PRAGMA foreign_key_check before committing.
// Every table in order must be present in data; table and column names are
// validated against the local schema.
func (db *DB) ReplaceTables(ctx context.Context, order []string, data []TableData, opt ReplaceOptions) error {
	byName := map[string]*TableData{}
	for i := range data {
		if !slices.Contains(order, data[i].Name) {
			return fmt.Errorf("unexpected table %q", data[i].Name)
		}
		byName[data[i].Name] = &data[i]
	}
	for _, t := range order {
		if byName[t] == nil {
			return fmt.Errorf("data lacks table %s (incompatible versions?)", t)
		}
	}
	conn, err := db.W.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if opt.Before != nil {
		if err := opt.Before(ctx, tx); err != nil {
			return err
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+order[i]); err != nil { //nolint:gosec // names from the caller's fixed list
			return err
		}
	}
	for _, t := range order {
		if err := insertRows(ctx, tx, byName[t]); err != nil {
			return fmt.Errorf("table %s: %w", t, err)
		}
	}
	if opt.After != nil {
		if err := opt.After(ctx, tx); err != nil {
			return err
		}
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertRows(ctx context.Context, tx *sql.Tx, td *TableData) error {
	local, err := tableColumns(ctx, tx, td.Name)
	if err != nil {
		return err
	}
	for _, c := range td.Columns {
		if !local[c] {
			return fmt.Errorf("unknown column %q (different schema version?)", c)
		}
	}
	if len(td.Rows) == 0 {
		return nil
	}
	// Table names come from the caller's fixed list, columns were checked
	// against the local schema above.
	q := `INSERT INTO ` + td.Name + ` (` + strings.Join(td.Columns, ", ") + `) VALUES (?` + strings.Repeat(", ?", len(td.Columns)-1) + `)` //nolint:gosec // see above
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	args := make([]any, len(td.Columns))
	for _, row := range td.Rows {
		if len(row) != len(td.Columns) {
			return errors.New("malformed row")
		}
		for i, v := range row {
			if args[i], err = sqlValue(v); err != nil {
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
	case nil, string, bool, int64, float64, []byte:
		return x, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		return x.Float64()
	case float32:
		return float64(x), nil
	case int:
		return int64(x), nil
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
		return fmt.Errorf("data violates a foreign key: %s row %d references %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}

// RemapLeases assigns every lease to the scope whose subnet contains its
// address and deletes leases outside all scopes. Run it after replacing the
// scopes table (scope ids may have changed).
func RemapLeases(ctx context.Context, tx *sql.Tx) error {
	const inScope = `leases.ip >= s.network AND leases.ip < s.network + (1 << (32 - s.prefix_len))`
	if _, err := tx.ExecContext(ctx, `DELETE FROM leases WHERE NOT EXISTS (SELECT 1 FROM scopes s WHERE `+inScope+`)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE leases SET scope_id = (SELECT s.id FROM scopes s WHERE `+inScope+`)`)
	return err
}

// DHCPConfigTables are the tables of the DHCP configuration in dependency
// order (referenced tables first).
var DHCPConfigTables = []string{
	"scopes", "superscopes", "superscope_members", "classes", "option_definitions",
	"exclusions", "reservations", "policies", "policy_conditions", "policy_ranges",
	"options", "mac_filters", "settings",
}

// ConfigTables are all replicated configuration tables: the DHCP
// configuration plus users and API tokens. Leases, audit log, sessions and
// HA state are local to each server.
var ConfigTables = append(append([]string{}, DHCPConfigTables...), "users", "api_tokens")

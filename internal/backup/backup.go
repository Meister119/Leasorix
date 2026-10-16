// Package backup creates and restores complete backups of a Leasorix
// server: the backup is a consistent copy of the SQLite database; a restore
// takes over configuration, users, API tokens and leases from such a copy
// (also from older versions, which are migrated first).
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/store"
)

// sqliteHeader starts every SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

// restoreTables are replaced by a restore (dependency order). The audit log,
// sessions and the HA state stay local.
var restoreTables = append(append([]string{}, store.ConfigTables...), "leases", "leases6")

// Create writes a consistent backup of the database to w (SQLite file,
// taken with VACUUM INTO while the server keeps running).
func Create(ctx context.Context, db *store.DB, w io.Writer) (int64, error) {
	dir, err := os.MkdirTemp(filepath.Dir(db.Path()), ".backup-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backup.db")
	if err := db.BackupTo(ctx, path); err != nil {
		return 0, err
	}
	f, err := os.Open(path) //nolint:gosec // our own temporary file
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(w, f)
}

// Summary describes a restored backup.
type Summary struct {
	SchemaVersion int64 `json:"schema_version"`
	Scopes        int   `json:"scopes"`
	Reservations  int   `json:"reservations"`
	Users         int   `json:"users"`
	Leases        int   `json:"leases"`
}

func invalid(format string, args ...any) error {
	var v apperr.Validator
	v.Add("backup", format, args...)
	return v.Err()
}

// Restore replaces configuration, users, API tokens and leases with the
// content of a backup read from r. All sessions end (everybody logs in
// again). The caller must reload the scope store and the lease manager.
func Restore(ctx context.Context, db *store.DB, r io.Reader) (*Summary, error) {
	dir, err := os.MkdirTemp(filepath.Dir(db.Path()), ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, store.FileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // temporary file in our directory
	if err != nil {
		return nil, err
	}
	head := make([]byte, len(sqliteHeader))
	n, _ := io.ReadFull(r, head)
	if n < len(head) || !bytes.Equal(head, sqliteHeader) {
		_ = f.Close()
		return nil, invalid("not a Leasorix backup (no SQLite database)")
	}
	if _, err := f.Write(head); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	src, err := store.Open(ctx, path)
	if err != nil {
		return nil, invalid("cannot open backup: %v", err)
	}
	defer src.Close()
	ver, err := checkSource(ctx, src)
	if err != nil {
		return nil, err
	}
	if err := src.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		return nil, fmt.Errorf("migrating backup: %w", err)
	}
	data, err := store.DumpTables(ctx, src.R, restoreTables)
	if err != nil {
		return nil, invalid("backup content: %v", err)
	}
	sum := &Summary{SchemaVersion: ver}
	for _, t := range data {
		switch t.Name {
		case "scopes":
			sum.Scopes = len(t.Rows)
		case "reservations":
			sum.Reservations = len(t.Rows)
		case "users":
			sum.Users = len(t.Rows)
		case "leases":
			sum.Leases = len(t.Rows)
		}
	}
	entry := audit.New(ctx, "system.restore", "system", "database", map[string]any{
		"schema_version": ver, "scopes": sum.Scopes, "reservations": sum.Reservations, "users": sum.Users, "leases": sum.Leases,
	})
	err = db.ReplaceTables(ctx, restoreTables, data, store.ReplaceOptions{
		After: func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
				return err
			}
			if err := store.RemapLeases(ctx, tx); err != nil {
				return err
			}
			return audit.Insert(ctx, tx, entry)
		},
	})
	if err != nil {
		return nil, err
	}
	return sum, nil
}

// checkSource verifies that the file is a Leasorix database this version
// can read and removes triggers and views (a crafted file must not run
// anything while being read; our own triggers are recreated by migrations
// where needed).
func checkSource(ctx context.Context, src *store.DB) (int64, error) {
	var n int
	err := src.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('goose_db_version', 'scopes', 'leases', 'users')`).Scan(&n)
	if err != nil {
		return 0, invalid("cannot read backup: %v", err)
	}
	if n != 4 {
		return 0, invalid("not a Leasorix backup")
	}
	var ver sql.NullInt64
	if err := src.R.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied`).Scan(&ver); err != nil {
		return 0, invalid("cannot read the schema version: %v", err)
	}
	latest, err := store.LatestSchemaVersion()
	if err != nil {
		return 0, err
	}
	if ver.Int64 > latest {
		return 0, invalid("the backup was created by a newer Leasorix version (schema %d, this version knows %d)", ver.Int64, latest)
	}
	rows, err := src.R.QueryContext(ctx, `SELECT type, name FROM sqlite_master WHERE type IN ('trigger', 'view')`)
	if err != nil {
		return 0, err
	}
	var drops []string
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		drops = append(drops, fmt.Sprintf(`DROP %s IF EXISTS "%s"`, typ, strings.ReplaceAll(name, `"`, `""`)))
	}
	_ = rows.Close()
	for _, q := range drops {
		if _, err := src.W.ExecContext(ctx, q); err != nil {
			return 0, err
		}
	}
	return ver.Int64, nil
}

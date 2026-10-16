// Package store owns the SQLite database: opening it with the right pragmas,
// running schema migrations and providing transaction helpers.
//
// SQLite allows a single writer at a time. To avoid "database is locked"
// errors the store keeps two pools: a writer pool limited to exactly one
// connection (all mutations are serialised in-process) and a reader pool for
// concurrent queries, which WAL mode allows alongside the writer.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// FileName is the database file name inside the data directory.
const FileName = "leasorix.db"

// DB bundles the writer and reader connection pools.
type DB struct {
	// W is the single-connection writer pool. Use it for every mutation.
	W *sql.DB
	// R is the reader pool. It is opened with query_only and rejects writes.
	R    *sql.DB
	path string
}

// Open opens (and creates if needed) the database at path. It does not run
// migrations; call Migrate afterwards.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	common := []string{
		"busy_timeout(10000)",
		"foreign_keys(1)",
		// FULL: an ACKed lease must survive power loss, otherwise the
		// address could be handed out twice after a crash.
		"synchronous(FULL)",
	}
	w, err := sql.Open("sqlite", dsn(path, append([]string{"journal_mode(WAL)"}, common...), "immediate"))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	r, err := sql.Open("sqlite", dsn(path, append([]string{"query_only(1)"}, common...), ""))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	r.SetMaxIdleConns(8)
	if err := r.PingContext(ctx); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("open database %s (reader): %w", path, err)
	}
	return &DB{W: w, R: r, path: path}, nil
}

func dsn(path string, pragmas []string, txlock string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if txlock != "" {
		q.Set("_txlock", txlock)
	}
	return "file:" + path + "?" + q.Encode()
}

// Path returns the file system path of the database.
func (db *DB) Path() string { return db.path }

// Close closes both pools.
func (db *DB) Close() error {
	return errors.Join(db.R.Close(), db.W.Close())
}

// Ping verifies that the database is reachable for reads and writes.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.W.PingContext(ctx); err != nil {
		return err
	}
	return db.R.PingContext(ctx)
}

// Migrate applies all pending migrations. It is idempotent.
func (db *DB) Migrate(ctx context.Context, log *slog.Logger) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db.W, sub)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, r := range results {
		log.Info("applied migration", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration.String())
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	log.Debug("database schema up to date", "version", v)
	return nil
}

// SchemaVersion returns the currently applied migration version.
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return 0, err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db.W, sub)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// WriteTx runs fn inside an IMMEDIATE write transaction. The transaction is
// committed when fn returns nil and rolled back otherwise.
func (db *DB) WriteTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// BackupTo writes a consistent copy of the database to path (which must not
// exist) using VACUUM INTO on a separate connection; writers are not
// blocked.
func (db *DB) BackupTo(ctx context.Context, path string) error {
	c, err := sql.Open("sqlite", dsn(db.path, []string{"busy_timeout(10000)"}, ""))
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// LatestSchemaVersion is the newest migration known to this binary.
func LatestSchemaVersion() (int64, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return 0, err
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return 0, err
	}
	var v int64
	for _, e := range entries {
		var n int64
		if _, err := fmt.Sscanf(e.Name(), "%05d_", &n); err == nil && n > v {
			v = n
		}
	}
	return v, nil
}

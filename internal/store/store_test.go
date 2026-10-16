package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openTemp(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "sub", FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	for i := range 3 {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, discardLogger()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		v, err := db.SchemaVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v < 1 {
			t.Fatalf("schema version = %d", v)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTablesExist(t *testing.T) {
	db := openTemp(t)
	for _, table := range []string{"scopes", "exclusions", "reservations", "options", "leases", "audit_log", "users"} {
		var name string
		err := db.R.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestPragmas(t *testing.T) {
	db := openTemp(t)
	var mode string
	if err := db.W.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %s", mode)
	}
	var fk int
	if err := db.R.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d", fk)
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	db := openTemp(t)
	_, err := db.R.Exec(`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES ('x','y','admin',0,0)`)
	if err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestWriteTxCommitRollbackAndCascade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO scopes (id, name, network, prefix_len, start_ip, end_ip, lease_time, created_at, updated_at)
			VALUES (1, 'lan', 167772160, 24, 167772170, 167772250, 3600, 0, 0)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO exclusions (scope_id, start_ip, end_ip) VALUES (1, 167772170, 167772180)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("boom")
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM scopes`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	var n int
	if err := db.R.QueryRow(`SELECT COUNT(*) FROM scopes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rollback failed: n=%d err=%v", n, err)
	}

	// CHECK constraint: start > end must fail.
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO exclusions (scope_id, start_ip, end_ip) VALUES (1, 10, 5)`)
		return err
	})
	if err == nil {
		t.Fatal("CHECK constraint not enforced")
	}

	// Deleting the scope cascades to its exclusions.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM scopes WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.R.QueryRow(`SELECT COUNT(*) FROM exclusions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade failed: n=%d err=%v", n, err)
	}
}

func TestOptionOwnerUniqueness(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	insert := func(q string) error {
		return db.WriteTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
	}
	if err := insert(`INSERT INTO options (code, type, value) VALUES (6, 'ip-list', '1.1.1.1')`); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO options (code, type, value) VALUES (6, 'ip-list', '8.8.8.8')`); err == nil {
		t.Fatal("duplicate server-level option accepted")
	}
}

func TestConcurrentWrites(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			errs <- db.WriteTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO audit_log (ts, actor, action, object_type) VALUES (?, 'test', 'x', 'y')`, i)
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.R.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestMigrationsDownAndUp(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO options (code, type, value) VALUES (6, 'ip-list', '1.1.1.1')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	var n int
	if err := db.R.QueryRow(`SELECT COUNT(*) FROM options WHERE policy_id IS NULL AND code = 6`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("option row lost across down/up: n=%d err=%v", n, err)
	}
}

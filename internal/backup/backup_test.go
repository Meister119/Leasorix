package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

func open(t *testing.T) (*store.DB, *scope.Store, *auth.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	s := scope.NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return db, s, auth.NewService(db, auth.Config{})
}

func count(t *testing.T, db *store.DB, table string) int {
	t.Helper()
	var n int
	if err := db.R.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBackupRestore(t *testing.T) {
	ctx := context.Background()
	db, s, a := open(t)
	sc, err := s.CreateScope(ctx, scope.Scope{Name: "lan", Subnet: netip.MustParsePrefix("10.70.0.0/24"), Start: netip.MustParseAddr("10.70.0.10"), End: netip.MustParseAddr("10.70.0.20"), LeaseTime: time.Hour, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReservation(ctx, scope.Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.70.0.5"), MAC: "02:00:00:00:00:05"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.W.ExecContext(ctx, `INSERT INTO leases (ip, scope_id, mac, state, starts_at, expires_at, updated_at) VALUES (?, ?, 'x', 'active', 0, 9999999999, 0)`, 0x0a46000b, sc.ID); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if n, err := Create(ctx, db, &buf); err != nil || n == 0 {
		t.Fatalf("backup: %d %v", n, err)
	}

	// Restore into another server with different content and a session.
	db2, s2, a2 := open(t)
	if _, err := s2.CreateScope(ctx, scope.Scope{Name: "other", Subnet: netip.MustParsePrefix("10.71.0.0/24"), Start: netip.MustParseAddr("10.71.0.10"), End: netip.MustParseAddr("10.71.0.20"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	u, err := a2.CreateUser(ctx, "someone", "another password!", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a2.CreateSession(ctx, u, "", ""); err != nil {
		t.Fatal(err)
	}
	sum, err := Restore(ctx, db2, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scopes != 1 || sum.Users != 1 || sum.Leases != 1 || sum.Reservations != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if err := s2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := s2.Snapshot().Pools(); len(p) != 1 || p[0].Name != "lan" {
		t.Fatal("configuration not restored")
	}
	if _, err := a2.Authenticate(ctx, "admin", "correct horse battery"); err != nil {
		t.Fatal("restored user cannot log in:", err)
	}
	if count(t, db2, "sessions") != 0 || count(t, db2, "leases") != 1 {
		t.Fatal("sessions must end, leases must be restored")
	}
	var action string
	if err := db2.R.QueryRow(`SELECT action FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action); err != nil || action != "system.restore" {
		t.Fatalf("audit: %q %v", action, err)
	}
	// Foreign keys are on again.
	if _, err := db2.W.ExecContext(ctx, `INSERT INTO exclusions (scope_id, start_ip, end_ip) VALUES (999, 1, 2)`); err == nil {
		t.Fatal("foreign keys still off")
	}
}

func TestRestoreRejectsInvalidFiles(t *testing.T) {
	ctx := context.Background()
	db, _, _ := open(t)
	var ve *apperr.ValidationError
	if _, err := Restore(ctx, db, bytes.NewReader([]byte("definitely not sqlite"))); !errors.As(err, &ve) {
		t.Fatalf("garbage: %v", err)
	}
	// A foreign SQLite database.
	other := filepath.Join(t.TempDir(), "x.db")
	odb, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := odb.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = odb.Close()
	f, err := readFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, db, bytes.NewReader(f)); !errors.As(err, &ve) {
		t.Fatalf("foreign database: %v", err)
	}
	// A backup from a newer version.
	src, _, _ := open(t)
	if _, err := src.W.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, 1)`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Create(ctx, src, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, db, &buf); !errors.As(err, &ve) {
		t.Fatalf("newer schema: %v", err)
	}
}

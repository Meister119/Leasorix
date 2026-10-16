package ha

import (
	"bytes"
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

func openDB(t *testing.T) (*store.DB, *scope.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx, quietLog()); err != nil {
		t.Fatal(err)
	}
	s := scope.NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return db, s
}

func TestConfigSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	ldb, lscopes := openDB(t)
	pdb, pscopes := openDB(t)

	// Leader configuration with dependent objects of every kind.
	sc, err := lscopes.CreateScope(ctx, scope.Scope{Name: "lan", Subnet: netip.MustParsePrefix("10.9.0.0/24"),
		Start: netip.MustParseAddr("10.9.0.10"), End: netip.MustParseAddr("10.9.0.200"), LeaseTime: time.Hour, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lscopes.AddExclusion(ctx, scope.Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("10.9.0.10"), End: netip.MustParseAddr("10.9.0.20")}); err != nil {
		t.Fatal(err)
	}
	if _, err := lscopes.CreateReservation(ctx, scope.Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.9.0.5"), MAC: "02:00:00:00:00:05", Hostname: "printer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lscopes.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: sc.ID}, Code: 6, Value: "10.9.0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lscopes.AddFilter(ctx, scope.MACFilter{List: "deny", Pattern: "02:aa:*"}); err != nil {
		t.Fatal(err)
	}
	la := auth.NewService(ldb, auth.Config{})
	admin, err := la.CreateUser(ctx, "admin", "correct horse battery", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := la.CreateToken(ctx, admin, "ci", auth.RoleOperator, 0); err != nil {
		t.Fatal(err)
	}

	// Partner: stale local configuration with a lease, plus a session of a
	// user that also exists on the leader (same id, same password).
	if _, err := pscopes.CreateScope(ctx, scope.Scope{Name: "old", Subnet: netip.MustParsePrefix("192.168.5.0/24"),
		Start: netip.MustParseAddr("192.168.5.10"), End: netip.MustParseAddr("192.168.5.20"), LeaseTime: time.Hour, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pdb.W.ExecContext(ctx, `INSERT INTO leases (ip, scope_id, mac, state, starts_at, expires_at, updated_at) VALUES (?, 1, 'x', 'active', 0, 0, 0)`,
		0xc0a8050a); err != nil {
		t.Fatal(err)
	}

	snap, err := dumpConfig(ctx, ldb)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyConfig(ctx, pdb, snap.Data); err != nil {
		t.Fatal(err)
	}
	again, err := dumpConfig(ctx, pdb)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Hash, snap.Hash) {
		t.Fatalf("partner configuration differs after apply:\n%s\n%s", snap.Data, again.Data)
	}
	var n int
	if err := pdb.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("lease outside the new configuration kept (%d, %v)", n, err)
	}
	if err := pscopes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := pscopes.Snapshot().Pools(); len(p) != 1 || p[0].Name != "lan" {
		t.Fatal("partner snapshot not rebuilt")
	}
	// Foreign keys are enforced again afterwards.
	if _, err := pdb.W.ExecContext(ctx, `INSERT INTO exclusions (scope_id, start_ip, end_ip) VALUES (999, 1, 2)`); err == nil {
		t.Fatal("foreign keys still disabled")
	}

	// Login on the partner works with the replicated user; a password
	// change on the leader ends the partner's sessions.
	pa := auth.NewService(pdb, auth.Config{})
	u, err := pa.Authenticate(ctx, "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := pa.CreateSession(ctx, u, "", "")
	if err != nil {
		t.Fatal(err)
	}
	snap2, _ := dumpConfig(ctx, ldb)
	if err := applyConfig(ctx, pdb, snap2.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.LookupSession(ctx, raw); err != nil {
		t.Fatal("unchanged user must keep the session:", err)
	}
	if err := la.ChangePassword(ctx, nil, admin.ID, "correct horse battery", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	snap3, _ := dumpConfig(ctx, ldb)
	if snap3.Version <= snap.Version {
		t.Fatal("config version not incremented by triggers")
	}
	if err := applyConfig(ctx, pdb, snap3.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.LookupSession(ctx, raw); err == nil {
		t.Fatal("session survived a password change on the leader")
	}
}

func TestConfigVersionIgnoresTokenUsage(t *testing.T) {
	ctx := context.Background()
	db, _ := openDB(t)
	a := auth.NewService(db, auth.Config{})
	u, err := a.CreateUser(ctx, "admin", "correct horse battery", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := a.CreateToken(ctx, u, "ci", auth.RoleOperator, 0)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := readConfigVersion(ctx, db)
	if _, err := a.LookupToken(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.CreateSession(ctx, u, "", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := readConfigVersion(ctx, db)
	if after != before {
		t.Fatalf("token use / login changed the config version (%d -> %d)", before, after)
	}
}

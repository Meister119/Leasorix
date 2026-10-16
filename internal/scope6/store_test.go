package scope6

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt6"
	"github.com/meister119/leasorix/internal/store"
)

func newStore(t *testing.T) *Store {
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
	s := NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func lan() Scope {
	return Scope{
		Name: "lan6", Prefix: netip.MustParsePrefix("2001:db8:1::/64"),
		Start: netip.MustParseAddr("2001:db8:1::100"), End: netip.MustParseAddr("2001:db8:1::1ff"),
		PreferredLifetime: time.Hour, ValidLifetime: 2 * time.Hour, Enabled: true,
		PDPool: netip.MustParsePrefix("2001:db8:100::/40"), PDLength: 56,
	}
}

func isValidation(err error) bool {
	var ve *apperr.ValidationError
	return errors.As(err, &ve)
}

func TestScopeValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for name, mod := range map[string]func(*Scope){
		"ipv4 prefix":     func(sc *Scope) { sc.Prefix = netip.MustParsePrefix("10.0.0.0/24") },
		"link local":      func(sc *Scope) { sc.Prefix = netip.MustParsePrefix("fe80::/64") },
		"host bits":       func(sc *Scope) { sc.Prefix = netip.MustParsePrefix("2001:db8:1::1/64") },
		"start outside":   func(sc *Scope) { sc.Start = netip.MustParseAddr("2001:db8:2::1") },
		"start after end": func(sc *Scope) { sc.Start, sc.End = sc.End, sc.Start },
		"only start":      func(sc *Scope) { sc.End = netip.Addr{} },
		"nothing to assign": func(sc *Scope) {
			sc.Start, sc.End, sc.PDPool, sc.PDLength = netip.Addr{}, netip.Addr{}, netip.Prefix{}, 0
		},
		"pd too short":      func(sc *Scope) { sc.PDLength = 40 },
		"pd too long":       func(sc *Scope) { sc.PDLength = 72 },
		"pd in link":        func(sc *Scope) { sc.PDPool = netip.MustParsePrefix("2001:db8:1::/56"); sc.PDLength = 60 },
		"preferred > valid": func(sc *Scope) { sc.PreferredLifetime = 3 * time.Hour },
		"valid too short":   func(sc *Scope) { sc.ValidLifetime, sc.PreferredLifetime = 30*time.Second, 10*time.Second },
	} {
		sc := lan()
		mod(&sc)
		if _, err := s.CreateScope(ctx, sc); !isValidation(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	sc, err := s.CreateScope(ctx, lan())
	if err != nil {
		t.Fatal(err)
	}
	// Overlaps with the prefix or the pool of another scope.
	other := lan()
	other.Name = "other"
	if _, err := s.CreateScope(ctx, other); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("overlap: %v", err)
	}
	other.Prefix, other.Start, other.End = netip.MustParsePrefix("2001:db8:2::/64"), netip.MustParseAddr("2001:db8:2::10"), netip.MustParseAddr("2001:db8:2::20")
	other.PDPool = netip.MustParsePrefix("2001:db8:1ff::/48")
	if _, err := s.CreateScope(ctx, other); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("pool overlap: %v", err)
	}
	other.PDPool, other.PDLength = netip.Prefix{}, 0
	if _, err := s.CreateScope(ctx, other); err != nil {
		t.Fatal(err)
	}
	// The prefix is immutable, the rest can change.
	sc.Name, sc.RapidCommit, sc.Preference = "lan6-renamed", true, 255
	if _, err := s.UpdateScope(ctx, sc); err != nil {
		t.Fatal(err)
	}
	sc.Prefix = netip.MustParsePrefix("2001:db8:3::/64")
	if _, err := s.UpdateScope(ctx, sc); !isValidation(err) {
		t.Fatalf("prefix change: %v", err)
	}
	got, _ := s.GetScope(ctx, sc.ID)
	if got.Name != "lan6-renamed" || !got.RapidCommit || got.Preference != 255 || got.PDPool.String() != "2001:db8:100::/40" || got.PDLength != 56 {
		t.Fatalf("stored %+v", got)
	}
}

func TestReservationsOptionsSnapshot(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddExclusion(ctx, Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("2001:db8:1::100"), End: netip.MustParseAddr("2001:db8:1::10f")}); err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, DUID: "00:01:00:01:2B:3C:4D:5E:02:00:00:00:00:01", IAID: AnyIAID, IP: netip.MustParseAddr("2001:db8:1::50"), Hostname: "nas"})
	if err != nil || r.DUID != "000100012b3c4d5e020000000001" {
		t.Fatalf("reservation %+v %v", r, err)
	}
	if _, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, DUID: r.DUID, IAID: 7, IP: netip.MustParseAddr("2001:db8:1::51")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, DUID: "0102", IAID: AnyIAID, IP: netip.MustParseAddr("2001:db8:1::52")}); !isValidation(err) {
		t.Fatalf("short DUID: %v", err)
	}
	if _, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, DUID: "aabbcc", IAID: AnyIAID, IP: netip.MustParseAddr("2001:db8:1::50")}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate address: %v", err)
	}
	for _, ov := range []OptionValue{
		{Owner: ServerOwner, Code: 23, Value: "2001:db8::53, 2001:DB8::54"},
		{Owner: Owner{OwnerScope, sc.ID}, Code: 24, Value: "lan.example"},
		{Owner: Owner{OwnerReservation, r.ID}, Code: 23, Value: "2001:db8::99"},
		{Owner: ServerOwner, Code: 1000, Type: dhcpopt6.TypeHex, Value: "01:02"},
	} {
		if _, err := s.SetOption(ctx, ov); err != nil {
			t.Fatalf("option %d: %v", ov.Code, err)
		}
	}
	for _, bad := range []OptionValue{
		{Owner: ServerOwner, Code: 3, Value: "x"},                             // managed
		{Owner: ServerOwner, Code: 23, Value: "10.0.0.1"},                     // not IPv6
		{Owner: ServerOwner, Code: 1001, Value: "x"},                          // unknown without type
		{Owner: ServerOwner, Code: 23, Type: dhcpopt6.TypeString, Value: "a"}, // wrong type
	} {
		if _, err := s.SetOption(ctx, bad); !isValidation(err) {
			t.Errorf("option %+v accepted: %v", bad, err)
		}
	}
	opts, _ := s.ListOptions(ctx, ServerOwner)
	if len(opts) != 2 || opts[0].Value != "2001:db8::53,2001:db8::54" {
		t.Fatalf("server options %+v", opts)
	}

	snap := s.Snapshot()
	p := snap.ForAddr(netip.MustParseAddr("2001:db8:1::abcd"))
	if p == nil || p.ID != sc.ID || snap.ForAddr(netip.MustParseAddr("2001:db8:2::1")) != nil {
		t.Fatal("ForAddr")
	}
	if snap.ForDelegated(netip.MustParseAddr("2001:db8:1ab::")) != p {
		t.Fatal("ForDelegated")
	}
	if !p.Excluded(netip.MustParseAddr("2001:db8:1::105")) || p.Excluded(netip.MustParseAddr("2001:db8:1::110")) || !p.InRange(netip.MustParseAddr("2001:db8:1::110")) {
		t.Fatal("exclusions / range")
	}
	if got := p.ReservationFor(r.DUID, 7); got == nil || got.IP.String() != "2001:db8:1::51" {
		t.Fatalf("exact IAID reservation %+v", got)
	}
	if got := p.ReservationFor(r.DUID, 1); got == nil || got.ID != r.ID {
		t.Fatalf("any-IAID reservation %+v", got)
	}
	o := snap.Options(p, p.ReservationFor(r.DUID, 1))
	if len(o[23]) != 16 || len(o[24]) == 0 || len(o[1000]) != 2 {
		t.Fatalf("merged options %v", o)
	}
	// Deleting the scope removes everything below it.
	if err := s.DeleteScope(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.ListReservations(ctx, 0); len(rs) != 0 {
		t.Fatal("reservations survived")
	}
	if os, _ := s.ListOptions(ctx, Owner{OwnerReservation, r.ID}); len(os) != 0 {
		t.Fatal("reservation options survived")
	}
}

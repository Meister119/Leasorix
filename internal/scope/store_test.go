package scope

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/ip4"
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
	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func lan(name, cidr, start, end string) Scope {
	return Scope{
		Name:      name,
		Subnet:    netip.MustParsePrefix(cidr),
		Start:     netip.MustParseAddr(start),
		End:       netip.MustParseAddr(end),
		LeaseTime: time.Hour,
		Enabled:   true,
	}
}

func isValidation(err error) bool {
	var ve *apperr.ValidationError
	return errors.As(err, &ve)
}

func TestScopeValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cases := map[string]Scope{
		"empty name":       lan("", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"),
		"host bits":        lan("a", "10.0.0.1/24", "10.0.0.10", "10.0.0.20"),
		"start outside":    lan("a", "10.0.0.0/24", "10.0.1.10", "10.0.0.20"),
		"end before start": lan("a", "10.0.0.0/24", "10.0.0.30", "10.0.0.20"),
		"network addr":     lan("a", "10.0.0.0/24", "10.0.0.0", "10.0.0.20"),
		"broadcast addr":   lan("a", "10.0.0.0/24", "10.0.0.10", "10.0.0.255"),
		"prefix too long":  lan("a", "10.0.0.0/31", "10.0.0.0", "10.0.0.1"),
		"prefix too short": lan("a", "10.0.0.0/7", "10.0.0.10", "10.0.0.20"),
		"ipv6":             {Name: "a", Subnet: netip.MustParsePrefix("2001:db8::/64")},
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateScope(ctx, sc); !isValidation(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
	bad := lan("a", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	bad.LeaseTime = time.Second
	if _, err := s.CreateScope(ctx, bad); !isValidation(err) {
		t.Fatalf("short lease accepted: %v", err)
	}
}

func TestScopeOverlapAndCRUD(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, err := s.CreateScope(ctx, lan("a", "10.0.0.0/24", "10.0.0.10", "10.0.0.200"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"10.0.0.0/16", "10.0.0.128/25"} {
		p := netip.MustParsePrefix(cidr)
		sc := Scope{Name: "x-" + cidr, Subnet: p, Start: p.Addr().Next(), End: p.Addr().Next().Next(), Enabled: true}
		if _, err := s.CreateScope(ctx, sc); !errors.Is(err, apperr.ErrConflict) {
			t.Errorf("%s: expected overlap conflict, got %v", cidr, err)
		}
	}
	if _, err := s.CreateScope(ctx, lan("a", "10.0.1.0/24", "10.0.1.10", "10.0.1.20")); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate name accepted: %v", err)
	}
	b, err := s.CreateScope(ctx, lan("b", "10.0.1.0/24", "10.0.1.10", "10.0.1.20"))
	if err != nil {
		t.Fatal(err)
	}

	snap := s.Snapshot()
	if len(snap.Pools()) != 2 {
		t.Fatalf("snapshot has %d pools", len(snap.Pools()))
	}
	if p := snap.ForAddr(ip4.MustFromAddr(netip.MustParseAddr("10.0.1.77"))); p == nil || p.ID != b.ID {
		t.Fatalf("ForAddr wrong: %+v", p)
	}
	if p := snap.ForAddr(ip4.MustFromAddr(netip.MustParseAddr("10.0.2.1"))); p != nil {
		t.Fatalf("ForAddr should be nil, got %s", p.Name)
	}
	if p := snap.ForAddr(ip4.MustFromAddr(netip.MustParseAddr("9.0.0.1"))); p != nil {
		t.Fatal("ForAddr below first scope")
	}

	a.Description = "changed"
	a.End = netip.MustParseAddr("10.0.0.100")
	if _, err := s.UpdateScope(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetScope(ctx, a.ID)
	if got.Description != "changed" || got.End != a.End {
		t.Fatalf("update not persisted: %+v", got)
	}
	a.Subnet = netip.MustParsePrefix("10.0.0.0/23")
	if _, err := s.UpdateScope(ctx, a); !isValidation(err) {
		t.Fatalf("subnet change accepted: %v", err)
	}

	if err := s.DeleteScope(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteScope(ctx, b.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if len(s.Snapshot().Pools()) != 1 {
		t.Fatal("snapshot not reloaded after delete")
	}
}

func TestExclusions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan("a", "192.168.1.0/24", "192.168.1.10", "192.168.1.200"))
	if err != nil {
		t.Fatal(err)
	}
	add := func(a, b string) error {
		_, err := s.AddExclusion(ctx, Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr(a), End: netip.MustParseAddr(b)})
		return err
	}
	if err := add("192.168.1.50", "192.168.1.60"); err != nil {
		t.Fatal(err)
	}
	if err := add("192.168.1.55", "192.168.1.70"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("overlap accepted: %v", err)
	}
	if err := add("192.168.1.5", "192.168.1.12"); !isValidation(err) {
		t.Fatalf("exclusion outside range accepted: %v", err)
	}
	if err := add("192.168.1.90", "192.168.1.80"); !isValidation(err) {
		t.Fatalf("reversed exclusion accepted: %v", err)
	}
	p := s.Snapshot().ByID(sc.ID)
	for ip, want := range map[string]bool{"192.168.1.49": false, "192.168.1.50": true, "192.168.1.60": true, "192.168.1.61": false} {
		if got := p.Excluded(ip4.MustFromAddr(netip.MustParseAddr(ip))); got != want {
			t.Errorf("Excluded(%s) = %v", ip, got)
		}
	}
	// Shrinking the range so that the exclusion falls outside is rejected.
	sc.End = netip.MustParseAddr("192.168.1.55")
	if _, err := s.UpdateScope(ctx, sc); !isValidation(err) {
		t.Fatalf("range shrink accepted: %v", err)
	}
	ex, _ := s.ListExclusions(ctx, sc.ID)
	if len(ex) != 1 {
		t.Fatalf("exclusions = %d", len(ex))
	}
	if err := s.DeleteExclusion(ctx, sc.ID, ex[0].ID); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().ByID(sc.ID).Excluded(ip4.MustFromAddr(netip.MustParseAddr("192.168.1.55"))) {
		t.Fatal("exclusion still in snapshot")
	}
}

func TestReservations(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan("a", "192.168.1.0/24", "192.168.1.10", "192.168.1.200"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.5"), MAC: "AA-BB-CC-DD-EE-01", Hostname: "printer"})
	if err != nil {
		t.Fatal(err)
	}
	if r.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("mac not normalised: %s", r.MAC)
	}
	cases := []struct {
		name string
		res  Reservation
		want func(error) bool
	}{
		{"duplicate ip", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.5"), MAC: "aa:bb:cc:dd:ee:02"}, func(e error) bool { return errors.Is(e, apperr.ErrConflict) }},
		{"duplicate mac", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.6"), MAC: "aa:bb:cc:dd:ee:01"}, func(e error) bool { return errors.Is(e, apperr.ErrConflict) }},
		{"outside subnet", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.2.6"), MAC: "aa:bb:cc:dd:ee:03"}, isValidation},
		{"broadcast", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.255"), MAC: "aa:bb:cc:dd:ee:03"}, isValidation},
		{"no identity", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.7")}, isValidation},
		{"bad mac", Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.7"), MAC: "xyz"}, isValidation},
		{"unknown scope", Reservation{ScopeID: 999, IP: netip.MustParseAddr("192.168.1.7"), MAC: "aa:bb:cc:dd:ee:04"}, func(e error) bool { return errors.Is(e, apperr.ErrNotFound) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.CreateReservation(ctx, c.res); !c.want(err) {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
	if _, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("192.168.1.20"), ClientID: "01:aa:bb:cc:dd:ee:09"}); err != nil {
		t.Fatal(err)
	}

	p := s.Snapshot().ByID(sc.ID)
	if got := p.ReservationFor("aa:bb:cc:dd:ee:01", ""); got == nil || got.Hostname != "printer" {
		t.Fatal("reservation by MAC not found")
	}
	if got := p.ReservationFor("ff:ff:ff:ff:ff:ff", "01aabbccddee09"); got == nil || got.IP != netip.MustParseAddr("192.168.1.20") {
		t.Fatal("reservation by client id not found")
	}
	if p.Dynamic(ip4.MustFromAddr(netip.MustParseAddr("192.168.1.20"))) {
		t.Fatal("reserved address must not be dynamic")
	}

	r.IP = netip.MustParseAddr("192.168.1.8")
	if _, err := s.UpdateReservation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().ByID(sc.ID).ReservationAt(ip4.MustFromAddr(netip.MustParseAddr("192.168.1.8"))) == nil {
		t.Fatal("updated reservation not in snapshot")
	}
	if err := s.DeleteReservation(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListReservations(ctx, 0)
	if len(all) != 1 {
		t.Fatalf("reservations left: %d", len(all))
	}

	// Deleting the scope cascades.
	if err := s.DeleteScope(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = s.ListReservations(ctx, 0)
	if len(all) != 0 {
		t.Fatal("reservations not cascaded")
	}
}

func TestOnChangeListener(t *testing.T) {
	s := newStore(t)
	var got uint64
	s.OnChange(func(sn *Snapshot) { got = sn.Version })
	if _, err := s.CreateScope(context.Background(), lan("a", "10.1.0.0/24", "10.1.0.10", "10.1.0.20")); err != nil {
		t.Fatal(err)
	}
	if got == 0 || got != s.Snapshot().Version {
		t.Fatalf("listener got version %d, snapshot %d", got, s.Snapshot().Version)
	}
}

func TestImportReservationsAllOrNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan("a", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	if err != nil {
		t.Fatal(err)
	}
	rows := []Reservation{
		{ScopeID: sc.ID, IP: netip.MustParseAddr("10.0.0.10"), MAC: "02:00:00:00:00:01", Hostname: "a"},
		{ScopeID: sc.ID, IP: netip.MustParseAddr("10.0.0.11"), MAC: "02:00:00:00:00:02"},
		{ScopeID: sc.ID, IP: netip.MustParseAddr("10.0.0.11"), MAC: "02:00:00:00:00:03"}, // duplicate IP within the batch
	}
	n, rowErrs, err := s.ImportReservations(ctx, rows)
	if err != nil || n != 0 || len(rowErrs) != 1 || rowErrs[0].Row != 2 {
		t.Fatalf("n=%d errs=%+v err=%v", n, rowErrs, err)
	}
	if all, _ := s.ListReservations(ctx, 0); len(all) != 0 {
		t.Fatal("partial import persisted")
	}
	rows[2].IP = netip.MustParseAddr("10.0.0.12")
	n, rowErrs, err = s.ImportReservations(ctx, rows)
	if err != nil || n != 3 || len(rowErrs) != 0 {
		t.Fatalf("n=%d errs=%+v err=%v", n, rowErrs, err)
	}
	if len(s.Snapshot().ByID(sc.ID).Reservations()) != 3 {
		t.Fatal("snapshot not reloaded")
	}
	// Normalisation errors are reported before touching the database.
	_, rowErrs, _ = s.ImportReservations(ctx, []Reservation{{ScopeID: sc.ID, IP: netip.MustParseAddr("10.0.0.13"), MAC: "nope"}})
	if len(rowErrs) != 1 || rowErrs[0].Row != 0 {
		t.Fatalf("errs %+v", rowErrs)
	}
}

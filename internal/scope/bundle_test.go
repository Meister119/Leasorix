package scope

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
	"time"
)

func TestBundleExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)
	sc := mustScope(t, src, "lan", "10.20.0.0/24", "10.20.0.10", "10.20.0.200")
	if _, err := src.AddExclusion(ctx, Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("10.20.0.50"), End: netip.MustParseAddr("10.20.0.59")}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.SetOption(ctx, OptionValue{Owner: Owner{Kind: OwnerScope, ID: sc.ID}, Code: 3, Value: "10.20.0.1"}); err != nil {
		t.Fatal(err)
	}
	r, err := src.CreateReservation(ctx, Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.20.0.5"), MAC: "02:00:00:00:00:05", Hostname: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.SetOption(ctx, OptionValue{Owner: Owner{Kind: OwnerReservation, ID: r.ID}, Code: 15, Value: "nas.example"}); err != nil {
		t.Fatal(err)
	}
	lt := 30 * time.Minute
	p, err := src.CreatePolicy(ctx, Policy{ScopeID: sc.ID, Name: "phones", Enabled: true, Operator: "or", LeaseTime: &lt,
		Conditions: []Condition{{Field: FieldVendorClass, Operator: OpEqual, Value: "Polycom*"}},
		Ranges:     []AddrRange{{Start: netip.MustParseAddr("10.20.0.150"), End: netip.MustParseAddr("10.20.0.160")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.SetOption(ctx, OptionValue{Owner: Owner{Kind: OwnerPolicy, ID: p.ID}, Code: 66, Value: "tftp.example"}); err != nil {
		t.Fatal(err)
	}

	b, err := src.ExportScope(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTestStore(t)
	if _, err := dst.ImportScope(ctx, b); err != nil {
		t.Fatal(err)
	}
	b2, err := dst.ExportScope(ctx, dst.Snapshot().Pools()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mustJSON(t, b2), mustJSON(t, b); got != want {
		t.Fatalf("round trip differs:\n%s\n%s", got, want)
	}

	// A failing part removes the whole import.
	bad := *b
	bad.Scope.Name, bad.Scope.Subnet, bad.Scope.Start, bad.Scope.End = "other", "10.21.0.0/24", "10.21.0.10", "10.21.0.200"
	bad.Exclusions = nil
	bad.Policies = nil
	bad.Reservations = []BundleReservation{{IP: "10.99.0.1", MAC: "02:00:00:00:00:09"}}
	if _, err := dst.ImportScope(ctx, &bad); err == nil {
		t.Fatal("reservation outside the subnet accepted")
	}
	if n := len(dst.Snapshot().Pools()); n != 1 {
		t.Fatalf("partial import left %d scopes", n)
	}
}

func TestSplitScope(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	sc := mustScope(t, s, "lan", "10.30.0.0/24", "10.30.0.1", "10.30.0.100")
	// An existing exclusion inside the partner part.
	if _, err := s.AddExclusion(ctx, Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("10.30.0.90"), End: netip.MustParseAddr("10.30.0.95")}); err != nil {
		t.Fatal(err)
	}
	res, err := s.SplitScope(ctx, sc.ID, 80)
	if err != nil {
		t.Fatal(err)
	}
	if res.LocalRange != (BundleRange{"10.30.0.1", "10.30.0.80"}) || res.PartnerRange != (BundleRange{"10.30.0.81", "10.30.0.100"}) {
		t.Fatalf("ranges %+v / %+v", res.LocalRange, res.PartnerRange)
	}
	p := s.Snapshot().ByID(sc.ID)
	for ip := uint32(0x0a1e0001); ip <= 0x0a1e0064; ip++ {
		if p.Dynamic(ip) != (ip <= 0x0a1e0050) {
			t.Fatalf("address .%d dynamic=%v after split", ip&0xff, p.Dynamic(ip))
		}
	}
	// The partner bundle excludes exactly the local part.
	dst := newTestStore(t)
	if _, err := dst.ImportScope(ctx, res.Partner); err != nil {
		t.Fatal(err)
	}
	q := dst.Snapshot().Pools()[0]
	for ip := uint32(0x0a1e0001); ip <= 0x0a1e0064; ip++ {
		want := ip > 0x0a1e0050 && (ip < 0x0a1e005a || ip > 0x0a1e005f)
		if q.Dynamic(ip) != want {
			t.Fatalf("partner address .%d dynamic=%v", ip&0xff, q.Dynamic(ip))
		}
	}
}

func newTestStore(t *testing.T) *Store { return newStore(t) }

func mustScope(t *testing.T, s *Store, name, cidr, start, end string) Scope {
	t.Helper()
	sc, err := s.CreateScope(context.Background(), lan(name, cidr, start, end))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

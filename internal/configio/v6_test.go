package configio

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/dhcpopt6"
	"github.com/meister119/leasorix/internal/scope6"
)

func TestIPv6ExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, src := openStore(t)
	s6 := scope6.NewStore(db)
	if err := s6.Load(ctx); err != nil {
		t.Fatal(err)
	}
	sc, err := s6.CreateScope(ctx, scope6.Scope{
		Name: "lan6", Prefix: netip.MustParsePrefix("2001:db8:1::/64"),
		Start: netip.MustParseAddr("2001:db8:1::100"), End: netip.MustParseAddr("2001:db8:1::1ff"),
		PreferredLifetime: time.Hour, ValidLifetime: 2 * time.Hour, RapidCommit: true, Preference: 200, Enabled: true,
		PDPool: netip.MustParsePrefix("2001:db8:100::/40"), PDLength: 56,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s6.AddExclusion(ctx, scope6.Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("2001:db8:1::100"), End: netip.MustParseAddr("2001:db8:1::10f")}); err != nil {
		t.Fatal(err)
	}
	r, err := s6.CreateReservation(ctx, scope6.Reservation{ScopeID: sc.ID, DUID: "000300010200000000aa", IAID: 3, IP: netip.MustParseAddr("2001:db8:1::50"), Hostname: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ov := range []scope6.OptionValue{
		{Owner: scope6.ServerOwner, Code: 23, Value: "2001:db8::53"},
		{Owner: scope6.ServerOwner, Code: 999, Type: dhcpopt6.TypeHex, Value: "abcd"},
		{Owner: scope6.Owner{Kind: scope6.OwnerScope, ID: sc.ID}, Code: 24, Value: "lan.example"},
		{Owner: scope6.Owner{Kind: scope6.OwnerReservation, ID: r.ID}, Code: 23, Value: "2001:db8::99"},
	} {
		if _, err := s6.SetOption(ctx, ov); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Export(ctx, src, s6, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	y := string(encode(t, d, true))
	for _, want := range []string{"scopes6:", "prefix: 2001:db8:1::/64", "pd_pool: 2001:db8:100::/40", "iaid: 3", "type: hex", "server_options6:"} {
		if !strings.Contains(y, want) {
			t.Fatalf("export lacks %q:\n%s", want, y)
		}
	}
	doc, err := Decode([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	db2, dst := openStore(t)
	dst6 := scope6.NewStore(db2)
	reloaded := false
	sum, err := Import(ctx, db2, dst, doc, Options{Mode: ModeReplace, Reload: func(ctx context.Context) error { reloaded = true; return dst6.Load(ctx) }})
	if err != nil || sum.Scopes6 != 1 || sum.Reservations != 1 || sum.Options != 4 || !reloaded {
		t.Fatalf("import %+v %v", sum, err)
	}
	d2, err := Export(ctx, dst, dst6, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encode(t, d2, true)); got != y {
		t.Fatalf("round trip differs:\n%s\n---\n%s", y, got)
	}
	if p := dst6.Snapshot().ForAddr(netip.MustParseAddr("2001:db8:1::5")); p == nil || !p.RapidCommit {
		t.Fatal("imported scope not in the snapshot")
	}
	// Merge: the same scope again is a conflict, nothing changes.
	if _, err := Import(ctx, db2, dst, doc, Options{Mode: ModeMerge}); err == nil {
		t.Fatal("duplicate IPv6 scope accepted")
	}
}

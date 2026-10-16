package configio

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

func openStore(t *testing.T) (*store.DB, *scope.Store) {
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
	return db, s
}

func encode(t *testing.T, d *Document, asYAML bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, d, asYAML); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func exportYAML(t *testing.T, s *scope.Store) string {
	t.Helper()
	d, err := Export(context.Background(), s, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return string(encode(t, d, true))
}

func seedConfig(t *testing.T, s *scope.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ss, err := s.CreateSuperscope(ctx, scope.Superscope{Name: "campus"})
	must(err)
	sc, err := s.CreateScope(ctx, scope.Scope{Name: "lan", Subnet: netip.MustParsePrefix("10.50.0.0/24"), Start: netip.MustParseAddr("10.50.0.10"),
		End: netip.MustParseAddr("10.50.0.200"), LeaseTime: time.Hour, Enabled: true, BOOTP: true, SuperscopeID: ss.ID})
	must(err)
	_, err = s.AddExclusion(ctx, scope.Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("10.50.0.10"), End: netip.MustParseAddr("10.50.0.19")})
	must(err)
	r, err := s.CreateReservation(ctx, scope.Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.50.0.5"), MAC: "02:00:00:00:00:05", Hostname: "nas"})
	must(err)
	_, err = s.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerReservation, ID: r.ID}, Code: 15, Value: "nas.example"})
	must(err)
	_, err = s.SetOption(ctx, scope.OptionValue{Owner: scope.ServerOwner, Code: 6, Value: "10.50.0.53"})
	must(err)
	_, err = s.CreateOptionDefinition(ctx, dhcpoptDef(224, "site-code"))
	must(err)
	_, err = s.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: sc.ID}, Code: 224, Value: "berlin"})
	must(err)
	_, err = s.CreateClass(ctx, scope.Class{Name: "phones", Type: scope.ClassVendor, Data: "Polycom*"})
	must(err)
	p, err := s.CreatePolicy(ctx, scope.Policy{Name: "phones", Enabled: true, Operator: "or",
		Conditions: []scope.Condition{{Field: scope.FieldClass, Operator: scope.OpEqual, Value: "phones"}}})
	must(err)
	_, err = s.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerPolicy, ID: p.ID}, Code: 66, Value: "tftp.example"})
	must(err)
	_, err = s.AddFilter(ctx, scope.MACFilter{List: scope.FilterDeny, Pattern: "02:aa:*"})
	must(err)
	must(s.SetFilterSettings(ctx, scope.FilterSettings{DenyEnabled: true}))
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, src := openStore(t)
	seedConfig(t, src)
	want := exportYAML(t, src)

	for _, asYAML := range []bool{true, false} {
		d, err := Export(ctx, src, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Decode(encode(t, d, asYAML))
		if err != nil {
			t.Fatal(err)
		}
		db, dst := openStore(t)
		sum, err := Import(ctx, db, dst, parsed, Options{Mode: ModeReplace})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Scopes != 1 || sum.Reservations != 1 || sum.Policies != 1 || sum.Classes != 1 || sum.OptionDefinitions != 1 {
			t.Fatalf("summary %+v", sum)
		}
		if got := exportYAML(t, dst); got != want {
			t.Fatalf("round trip (yaml=%v) differs:\n%s\n---\n%s", asYAML, got, want)
		}
	}
}

func TestImportMergeConflictsAndDryRun(t *testing.T) {
	ctx := context.Background()
	_, src := openStore(t)
	seedConfig(t, src)
	d, err := Export(ctx, src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, dst := openStore(t)
	if _, err := Import(ctx, db, dst, d, Options{Mode: ModeMerge}); err != nil {
		t.Fatal(err)
	}
	before := exportYAML(t, dst)
	// Importing the same document again conflicts (scope exists).
	_, err = Import(ctx, db, dst, d, Options{Mode: ModeMerge})
	if !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("second merge: %v", err)
	}
	if exportYAML(t, dst) != before {
		t.Fatal("failed import changed the configuration")
	}
	// Dry run of a valid merge: nothing changes.
	extra := &Document{Format: Format, Scopes: []scope.Bundle{{Scope: scope.BundleScope{Name: "guest", Subnet: "10.51.0.0/24", Start: "10.51.0.10", End: "10.51.0.20", LeaseTime: 600, Enabled: true}}}}
	sum, err := Import(ctx, db, dst, extra, Options{Mode: ModeMerge, DryRun: true})
	if err != nil || sum.Scopes != 1 || !sum.DryRun {
		t.Fatalf("dry run: %+v %v", sum, err)
	}
	if exportYAML(t, dst) != before {
		t.Fatal("dry run changed the configuration")
	}
	if _, err := Import(ctx, db, dst, extra, Options{Mode: ModeMerge}); err != nil {
		t.Fatal(err)
	}
	if n := len(dst.Snapshot().Pools()); n != 2 {
		t.Fatalf("%d scopes after merge", n)
	}
	// Validation errors name the object.
	bad := &Document{Format: Format, Scopes: []scope.Bundle{{Scope: scope.BundleScope{Name: "bad", Subnet: "10.52.0.0/24", Start: "10.53.0.1", End: "10.52.0.20", Enabled: true}}}}
	_, err = Import(ctx, db, dst, bad, Options{Mode: ModeMerge})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), `scope "bad"`) {
		t.Fatalf("validation error: %v", err)
	}
}

func TestReplaceKeepsLeasesInCoveredSubnets(t *testing.T) {
	ctx := context.Background()
	db, s := openStore(t)
	seedConfig(t, s)
	// Leases in the scope and in a scope that will disappear.
	other, err := s.CreateScope(ctx, scope.Scope{Name: "old", Subnet: netip.MustParsePrefix("10.60.0.0/24"), Start: netip.MustParseAddr("10.60.0.10"), End: netip.MustParseAddr("10.60.0.20"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	lan := s.Snapshot().Pools()[0]
	for _, l := range []struct {
		ip    uint32
		scope int64
	}{{0x0a32000b, lan.ID}, {0x0a3c000b, other.ID}} {
		if _, err := db.W.ExecContext(ctx, `INSERT INTO leases (ip, scope_id, mac, state, starts_at, expires_at, updated_at) VALUES (?, ?, 'x', 'active', 0, 9999999999, 0)`, l.ip, l.scope); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Export(ctx, s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.Scopes = d.Scopes[:1] // only "lan" stays (scopes are ordered by network)
	if _, err := Import(ctx, db, s, d, Options{Mode: ModeReplace}); err != nil {
		t.Fatal(err)
	}
	var ip, scopeID int64
	rows, err := db.R.QueryContext(ctx, `SELECT ip, scope_id FROM leases`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&ip, &scopeID); err != nil {
			t.Fatal(err)
		}
	}
	newLan := s.Snapshot().Pools()[0]
	if n != 1 || ip != 0x0a32000b || scopeID != newLan.ID {
		t.Fatalf("leases after replace: n=%d ip=%x scope=%d (lan=%d)", n, ip, scopeID, newLan.ID)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	if _, err := Decode([]byte("format: leasorix-config/v1\nscopez: []\n")); err == nil {
		t.Fatal("unknown YAML key accepted")
	}
	if _, err := Decode([]byte(`{"format":"leasorix-config/v1","x":1}`)); err == nil {
		t.Fatal("unknown JSON key accepted")
	}
	if _, err := Decode([]byte("format: other\n")); err == nil {
		t.Fatal("wrong format accepted")
	}
}

func TestNetshImport(t *testing.T) {
	raw, err := os.ReadFile("testdata/netsh_dump.txt")
	if err != nil {
		t.Fatal(err)
	}
	// PowerShell writes UTF-16LE with BOM.
	u := utf16.Encode([]rune(string(raw)))
	b := []byte{0xff, 0xfe}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	for _, input := range [][]byte{raw, b} {
		d, warnings, err := ParseNetsh(bytes.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		reasons := ""
		for _, w := range warnings {
			reasons += w.Reason + "\n"
		}
		for _, want := range []string{"class-specific option values", "managed by Leasorix", "multicast", "DHCPv6", "create the DNS zones", "vendor-specific option definitions"} {
			if !strings.Contains(reasons, want) {
				t.Errorf("missing warning %q in\n%s", want, reasons)
			}
		}
		ctx := context.Background()
		db, s := openStore(t)
		if _, err := Import(ctx, db, s, d, Options{Mode: ModeMerge}); err != nil {
			t.Fatal(err)
		}
		pools := s.Snapshot().Pools()
		if len(pools) != 2 {
			t.Fatalf("%d scopes", len(pools))
		}
		office, lab := pools[0], pools[1]
		if office.Name != "Office" || office.Start.String() != "10.10.1.20" || office.LeaseTime != 8*24*time.Hour || !office.Enabled || office.BOOTP {
			t.Fatalf("office %+v", office.Scope)
		}
		if !office.Excluded(0x0a0a0119) || office.Excluded(0x0a0a011f) {
			t.Fatal("exclusion not imported")
		}
		if lab.Subnet.String() != "10.10.2.0/25" || lab.LeaseTime != 0 || lab.Enabled || !lab.BOOTP {
			t.Fatalf("lab %+v", lab.Scope)
		}
		if office.SuperscopeID == 0 || office.SuperscopeID != lab.SuperscopeID {
			t.Fatal("superscope not imported")
		}
		// Server "set dnsconfig 1 0 1 0" is the default, the lab scope has
		// its own "1 1 0 0".
		if office.DNS.Mode != scope.DNSClient || !office.DNS.Cleanup || !office.DNS.Protect {
			t.Fatalf("office dns %+v", office.DNS)
		}
		if lab.DNS.Mode != scope.DNSAlways || lab.DNS.Cleanup {
			t.Fatalf("lab dns %+v", lab.DNS)
		}
		res := office.ReservationFor("00:15:5d:0a:0b:0c", "")
		if res == nil || res.Hostname != "printer01.example.local" || res.Description != "Printer 1st floor" {
			t.Fatalf("reservation %+v", res)
		}
		exp, err := Export(ctx, s, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		y := string(encode(t, exp, true))
		for _, want := range []string{"name: site-code", "code: 224", "value: http://wpad.example.local/wpad.dat", "value: 10.10.0.10,10.10.0.11", "name: IP-Phones", "data: Polycom",
			"pattern: 00:15:5d:01:02:03", "mac_filter_deny_enabled: true", "value: 10.10.1.254"} {
			if !strings.Contains(y, want) {
				t.Errorf("export lacks %q:\n%s", want, y)
			}
		}
		if strings.Contains(y, "Default BOOTP Class") {
			t.Error("Windows default class imported")
		}
	}
}

func TestNetshRejectsOtherInput(t *testing.T) {
	if _, _, err := ParseNetsh(strings.NewReader("hello world\n")); err == nil {
		t.Fatal("garbage accepted")
	}
}

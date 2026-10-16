package configio

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/scope"
)

const secret = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestDNSExportImport(t *testing.T) {
	ctx := context.Background()
	_, src := openStore(t)
	k, err := src.CreateDNSKey(ctx, scope.DNSKey{Name: "leasorix-key", Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateDNSZone(ctx, scope.DNSZone{Name: "lan.example", Server: "192.0.2.53", KeyID: k.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateDNSZone(ctx, scope.DNSZone{Name: "0.10.in-addr.arpa", Server: "192.0.2.53:5353"}); err != nil {
		t.Fatal(err)
	}
	dns := scope.DNSSettings{Mode: scope.DNSClient, Domain: "lan.example", TTL: 5 * time.Minute, Cleanup: false, Protect: true}
	if _, err := src.CreateScope(ctx, scope.Scope{Name: "lan", Subnet: netip.MustParsePrefix("10.0.0.0/24"),
		Start: netip.MustParseAddr("10.0.0.10"), End: netip.MustParseAddr("10.0.0.20"), LeaseTime: time.Hour, Enabled: true, DNS: dns}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateScope(ctx, scope.Scope{Name: "plain", Subnet: netip.MustParsePrefix("10.1.0.0/24"),
		Start: netip.MustParseAddr("10.1.0.10"), End: netip.MustParseAddr("10.1.0.20"), LeaseTime: time.Hour, Enabled: true, DNS: scope.DefaultDNSSettings()}); err != nil {
		t.Fatal(err)
	}
	y := exportYAML(t, src)
	if !strings.Contains(y, "tsig_key: leasorix-key") || strings.Contains(y, secret) || !strings.Contains(y, "mode: client") {
		t.Fatalf("export:\n%s", y)
	}
	if strings.Count(y, "dns:") != 1 {
		t.Fatalf("default DNS settings should be omitted:\n%s", y)
	}
	doc, err := Decode([]byte(y))
	if err != nil {
		t.Fatal(err)
	}

	// The key does not exist on the target: clear error, nothing imported.
	db, dst := openStore(t)
	_, err = Import(ctx, db, dst, doc, Options{Mode: ModeMerge})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "leasorix-key") {
		t.Fatalf("missing key: %v", err)
	}
	if scs, _ := dst.ListScopes(ctx); len(scs) != 0 {
		t.Fatal("partial import")
	}
	// With the key, the import reproduces the configuration.
	if _, err := dst.CreateDNSKey(ctx, scope.DNSKey{Name: "LEASORIX-KEY", Secret: secret}); err != nil {
		t.Fatal(err)
	}
	sum, err := Import(ctx, db, dst, doc, Options{Mode: ModeReplace})
	if err != nil || sum.DNSZones != 2 || sum.Scopes != 2 {
		t.Fatalf("import: %+v %v", sum, err)
	}
	if got := exportYAML(t, dst); got != y {
		t.Fatalf("round trip differs:\n%s\n---\n%s", y, got)
	}
	scs, _ := dst.ListScopes(ctx)
	if scs[0].DNS != dns || scs[1].DNS != scope.DefaultDNSSettings() {
		t.Fatalf("scope dns settings %+v / %+v", scs[0].DNS, scs[1].DNS)
	}
	if dst.Snapshot().DNSTargetFor("pc.lan.example") == nil {
		t.Fatal("imported zone not in the snapshot")
	}
}

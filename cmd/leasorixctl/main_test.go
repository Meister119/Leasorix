package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meister119/leasorix/internal/app"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/config"
)

type server struct {
	url   string
	token string
	dir   string
}

func startServer(t *testing.T) *server {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TLS.Mode = config.TLSOff
	cfg.DHCP4.ConflictDetection.Enabled = false
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, &cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	addr, err := a.HTTP().Addr(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.Auth().CreateUser(ctx, "admin", "correct horse battery", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := a.Auth().CreateToken(ctx, u, "ctl", auth.RoleAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &server{url: "http://" + addr.String(), token: tok, dir: t.TempDir()}
}

func (s *server) ctl(t *testing.T, wantCode int, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	env := func(k string) string {
		switch k {
		case "LEASORIX_URL":
			return s.url
		case "LEASORIX_TOKEN":
			return s.token
		}
		return ""
	}
	code := run(context.Background(), args, &out, &errOut, env)
	if code != wantCode {
		t.Fatalf("leasorixctl %s: exit %d (want %d)\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, wantCode, out.String(), errOut.String())
	}
	return out.String() + errOut.String()
}

func (s *server) file(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(s.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestLeasorixctl(t *testing.T) {
	s := startServer(t)
	contains(t, s.ctl(t, 0, "version"), "leasorixctl")
	contains(t, s.ctl(t, 0, "status"), "HA:             not configured", "SCOPE")

	doc := s.file(t, "config.yaml", `format: leasorix-config/v1
scopes:
  - scope: {name: lan, subnet: 10.80.0.0/24, start: 10.80.0.10, end: 10.80.0.200, lease_time: 3600, enabled: true}
    exclusions: []
    reservations: [{ip: 10.80.0.5, mac: "02:00:00:00:00:05", hostname: nas}]
    options: [{code: 3, value: 10.80.0.1}]
    policies: []
`)
	contains(t, s.ctl(t, 0, "config", "import", doc, "--dry-run"), "dry run", "1 scopes", "1 reservations")
	if strings.Contains(s.ctl(t, 0, "scopes", "list"), "lan") {
		t.Fatal("dry run created the scope")
	}
	contains(t, s.ctl(t, 0, "config", "import", doc), "imported (merge): 1 scopes")
	contains(t, s.ctl(t, 0, "scopes", "list"), "lan", "10.80.0.0/24", "10.80.0.10 - 10.80.0.200", "1h0m0s")
	contains(t, s.ctl(t, 0, "scopes", "show", "lan"), "Subnet:       10.80.0.0/24", "191 usable")
	contains(t, s.ctl(t, 1, "config", "import", doc), "server answered 409", "lan")
	zones := s.file(t, "zones.yaml", "format: leasorix-config/v1\ndns_zones: [{name: lan.example, server: 192.0.2.53}]\n")
	contains(t, s.ctl(t, 0, "config", "import", zones), "1 DNS zones")
	contains(t, s.ctl(t, 0, "dns", "zones"), "lan.example", "192.0.2.53", "(unsigned)")
	contains(t, s.ctl(t, 0, "dns", "registrations"), "IP", "STATE")
	v6 := s.file(t, "v6.yaml", `format: leasorix-config/v1
scopes6:
  - {name: lan6, prefix: "2001:db8:5::/64", start: "2001:db8:5::100", end: "2001:db8:5::1ff", preferred_lifetime: 1800, valid_lifetime: 3600,
     enabled: true, pd_pool: "2001:db8:500::/40", pd_length: 56}
`)
	contains(t, s.ctl(t, 0, "config", "import", v6), "1 IPv6 scopes")
	contains(t, s.ctl(t, 0, "v6", "scopes"), "lan6", "2001:db8:5::/64", "2001:db8:5::100 - 2001:db8:5::1ff", "2001:db8:500::/40 /56", "30m0s/1h0m0s")
	contains(t, s.ctl(t, 0, "v6", "leases", "--scope", "lan6"), "ADDRESS", "DUID")
	contains(t, s.ctl(t, 1, "v6", "leases", "--scope", "nope"), "no IPv6 scope")
	contains(t, s.ctl(t, 1, "v6", "leases", "delete", "2001:db8:5::100"), "server answered 404")
	contains(t, s.ctl(t, 2, "v6", "bogus"), "unknown command")

	bundle := filepath.Join(s.dir, "lan.json")
	s.ctl(t, 0, "scopes", "export", "lan", "-f", bundle)
	if b, _ := os.ReadFile(bundle); !strings.Contains(string(b), "leasorix-scope/v1") {
		t.Fatal("scope bundle not written")
	}
	contains(t, s.ctl(t, 0, "config", "export", "--format", "json"), `"format": "leasorix-config/v1"`)
	contains(t, s.ctl(t, 0, "-o", "json", "scopes", "list"), `"subnet": "10.80.0.0/24"`)
	contains(t, s.ctl(t, 0, "leases", "list", "--scope", "lan"), "IP", "MAC")
	contains(t, s.ctl(t, 1, "leases", "delete", "10.80.0.99"), "server answered 404")
	contains(t, s.ctl(t, 0, "ha", "status"), "not configured")
	contains(t, s.ctl(t, 1, "ha", "partner-down", "--yes"), "409")
	contains(t, s.ctl(t, 2, "ha", "partner-down"), "--yes")

	leases := filepath.Join(s.dir, "leases.json")
	s.ctl(t, 0, "leases", "export", "-f", leases)
	contains(t, s.ctl(t, 0, "leases", "import", leases), "records read")

	backupFile := filepath.Join(s.dir, "backup.db")
	contains(t, s.ctl(t, 0, "backup", backupFile), "backup written")
	contains(t, s.ctl(t, 2, "restore", backupFile), "--yes")
	contains(t, s.ctl(t, 0, "restore", backupFile, "--yes"), "restored 1 scopes, 1 reservations, 1 users")

	dump, err := os.ReadFile("../../internal/configio/testdata/netsh_dump.txt")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, s.ctl(t, 0, "config", "import", "--netsh", "--mode", "replace", s.file(t, "dump.txt", string(dump))), "imported (replace): 2 scopes", "line ")
	contains(t, s.ctl(t, 0, "scopes", "list"), "Office", "Lab")
}

func TestLeasorixctlUsage(t *testing.T) {
	s := startServer(t)
	s.ctl(t, 2)
	contains(t, s.ctl(t, 2, "frobnicate"), "unknown command")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"scopes", "list"}, &out, &errOut, func(string) string { return "" }); code != 2 || !strings.Contains(errOut.String(), "API token") {
		t.Fatalf("missing token: %d %s", code, errOut.String())
	}
	bad := *s
	bad.token = "lxt_invalid"
	contains(t, bad.ctl(t, 1, "scopes", "list"), "server answered 401")
}

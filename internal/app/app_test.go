package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/scope"
)

func TestAppStartsAndStops(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.HTTP.Listen = "127.0.0.1:0"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, &cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	addrCtx, addrCancel := context.WithTimeout(ctx, 5*time.Second)
	defer addrCancel()
	addr, err := a.HTTP().Addr(addrCtx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr.String() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app did not stop")
	}

	// A second start against the same data dir must succeed (idempotent migrations).
	ctx2, cancel2 := context.WithCancel(context.Background())
	a2, err := New(ctx2, &cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	cancel2()
	if err := a2.Run(ctx2); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsExposeScopeStatistics(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.HTTP.Listen = "127.0.0.1:0"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, &cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()

	if _, err := a.Scopes().CreateScope(ctx, scope.Scope{
		Name: "lan", Subnet: netip.MustParsePrefix("10.0.0.0/24"),
		Start: netip.MustParseAddr("10.0.0.10"), End: netip.MustParseAddr("10.0.0.19"), LeaseTime: time.Hour, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	addrCtx, addrCancel := context.WithTimeout(ctx, 5*time.Second)
	defer addrCancel()
	addr, err := a.HTTP().Addr(addrCtx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr.String() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, want := range []string{
		`leasorix_scope_addresses{kind="usable",scope="lan",subnet="10.0.0.0/24"} 10`,
		`leasorix_scope_addresses{kind="available",scope="lan",subnet="10.0.0.0/24"} 10`,
		`leasorix_scope_utilization_ratio{scope="lan",subnet="10.0.0.0/24"} 0`,
		`leasorix_leases{scope="lan",state="active",subnet="10.0.0.0/24"} 0`,
		`leasorix_lease_commit_duration_seconds_count`,
		`leasorix_build_info`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	entries, err := a.Audit().List(ctx, audit.Filter{})
	if err != nil || len(entries) != 1 || entries[0].Action != "scope.create" {
		t.Fatalf("audit: %+v %v", entries, err)
	}
}

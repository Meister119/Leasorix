package lease

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseARP(t *testing.T) {
	const table = `IP address       HW type     Flags       HW address            Mask     Device
10.0.0.5         0x1         0x2         02:00:00:00:00:05     *        eth0
10.0.0.6         0x1         0x0         00:00:00:00:00:00     *        eth0
`
	if got := parseARP(bufio.NewScanner(strings.NewReader(table)), "10.0.0.5"); got != "02:00:00:00:00:05" {
		t.Fatalf("got %q", got)
	}
	if got := parseARP(bufio.NewScanner(strings.NewReader(table)), "10.0.0.6"); got != "" {
		t.Fatalf("incomplete entry returned %q", got)
	}
}

// TestICMPProberLoopback pings 127.0.0.1 (always answers) and an address in
// TEST-NET-1 (never answers). Skipped when no ICMP socket can be opened.
func TestICMPProberLoopback(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := NewICMPProber(log, 300*time.Millisecond, 1)
	if err != nil {
		t.Skipf("no ICMP socket available: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	if !p.InUse(ctx, ip("127.0.0.1"), "") {
		t.Fatal("loopback did not answer")
	}
	start := time.Now()
	if p.InUse(ctx, ip("192.0.2.123"), "") {
		t.Fatal("TEST-NET address reported in use")
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("timeout not honoured")
	}
}

//go:build integration && linux

package integration

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
	"github.com/meister119/leasorix/test/dhcpclient"
)

func (tp *topo) scrape() string {
	tp.t.Helper()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: tp.srv.DialContext}}
	resp, err := client.Get("http://10.77.0.1:8067/metrics")
	if err != nil {
		tp.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestMetricsAndAuditAfterTraffic: after real DHCP exchanges the packet,
// latency, lease and scope metrics are exposed and lease events are in the
// audit log.
func TestMetricsAndAuditAfterTraffic(t *testing.T) {
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.109", time.Hour)
	})
	tp.startServer("")
	conn := tp.conn()
	ctx := context.Background()
	for range 3 {
		c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
		ack, err := c.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Release(ctx, ack); err != nil {
			t.Fatal(err)
		}
	}
	// One more active lease that stays.
	stay := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	if _, err := stay.DORA(ctx); err != nil {
		t.Fatal(err)
	}

	var body string
	waitFor(t, 5*time.Second, "metrics", func() bool {
		body = tp.scrape()
		return strings.Contains(body, `leasorix_lease_events_total{event="release"} 3`)
	})
	for _, want := range []string{
		`leasorix_dhcp4_packets_received_total{type="DISCOVER"} 4`,
		`leasorix_dhcp4_packets_received_total{type="REQUEST"} 4`,
		`leasorix_dhcp4_packets_received_total{type="RELEASE"} 3`,
		`leasorix_dhcp4_packets_sent_total{type="OFFER"} 4`,
		`leasorix_dhcp4_packets_sent_total{type="ACK"} 4`,
		`leasorix_dhcp4_request_duration_seconds_count{type="REQUEST"} 4`,
		`leasorix_lease_events_total{event="assign"} 4`,
		`leasorix_scope_addresses{kind="in_use",scope="lan",subnet="10.77.0.0/16"} 1`,
		`leasorix_scope_addresses{kind="usable",scope="lan",subnet="10.77.0.0/16"} 10`,
		`leasorix_leases{scope="lan",state="released",subnet="10.77.0.0/16"} 3`,
		`leasorix_scope_utilization_ratio{scope="lan",subnet="10.77.0.0/16"} 0.1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}

	db, err := store.Open(ctx, tp.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := audit.NewLog(db).List(ctx, audit.Filter{Action: "lease."})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Action]++
		if e.Actor != audit.ActorDHCP || e.Source != "s0" {
			t.Errorf("unexpected actor/source %+v", e)
		}
	}
	if counts["lease.assign"] != 4 || counts["lease.release"] != 3 {
		t.Fatalf("audited lease events %v", counts)
	}
}

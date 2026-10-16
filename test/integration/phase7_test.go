//go:build integration && linux

package integration

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/test/dhcpclient"
)

// TestSuperscopeMultinet: the server has an address only in 10.77.0.0/16;
// a second subnet on the same wire is reachable through a superscope. When
// the first scope is exhausted, clients get addresses of the second one with
// its own options.
func TestSuperscopeMultinet(t *testing.T) {
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		ss, err := s.CreateSuperscope(ctx, scope.Superscope{Name: "segment-1"})
		if err != nil {
			t.Fatal(err)
		}
		a := mustScope(t, s, "primary", "10.77.0.0/16", "10.77.1.0", "10.77.1.1", time.Hour)
		b := mustScope(t, s, "secondary", "10.78.0.0/24", "10.78.0.10", "10.78.0.200", time.Hour)
		for _, sc := range []scope.Scope{a, b} {
			sc.SuperscopeID = ss.ID
			if _, err := s.UpdateScope(ctx, sc); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: b.ID}, Code: 3, Value: "10.78.0.1"}); err != nil {
			t.Fatal(err)
		}
	})
	tp.startServer("")
	conn := tp.conn()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var got []string
	for range 3 {
		c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
		ack, err := c.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ack.YourIPAddr.String())
		if ack.YourIPAddr.String() == "10.78.0.10" {
			if r := ack.Router(); len(r) != 1 || !r[0].Equal(net.ParseIP("10.78.0.1")) || ack.SubnetMask().String() != "ffffff00" {
				t.Fatalf("options of the secondary subnet: %s", ack.Summary())
			}
			if !ack.ServerIdentifier().Equal(net.ParseIP("10.77.0.1")) {
				t.Fatalf("server id %s", ack.ServerIdentifier())
			}
		}
	}
	if got[0] != "10.77.1.0" || got[1] != "10.77.1.1" || got[2] != "10.78.0.10" {
		t.Fatalf("addresses %v", got)
	}
}

// TestBOOTPClients: a BOOTP client with a reservation and one on a scope
// with dynamic BOOTP get BOOTREPLYs; without either no answer.
func TestBOOTPClients(t *testing.T) {
	tp := newTopo(t)
	reserved := dhcpclient.RandomMAC()
	tp.seed(func(ctx context.Context, s *scope.Store) {
		sc := mustScope(t, s, "lan", "10.77.0.0/16", "10.77.1.0", "10.77.1.100", time.Hour)
		if _, err := s.CreateReservation(ctx, scope.Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.77.0.50"), MAC: reserved.String(), Hostname: "legacy"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: sc.ID}, Code: 67, Value: "pxelinux.0"}); err != nil {
			t.Fatal(err)
		}
	})
	tp.startServer("  bootp_lease_time: 48h\n")
	conn := tp.conn()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	boot := func(mac net.HardwareAddr) (*dhcpv4.DHCPv4, error) {
		m, err := dhcpv4.New(dhcpv4.WithHwAddr(mac), dhcpv4.WithBroadcast(true))
		if err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Exchange(short, m, dhcpv4.MessageTypeNone)
	}
	r, err := boot(reserved)
	if err != nil {
		t.Fatal(err)
	}
	if r.OpCode != dhcpv4.OpcodeBootReply || r.MessageType() != dhcpv4.MessageTypeNone || r.YourIPAddr.String() != "10.77.0.50" || r.BootFileName != "pxelinux.0" {
		t.Fatalf("reply %s", r.Summary())
	}
	if l, ok := tp.leaseFor("10.77.0.50"); !ok || l.State != "active" {
		t.Fatalf("BOOTP lease not stored: %+v", l)
	} else if d := time.Until(time.Unix(l.Expires, 0)); d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("BOOTP lease time %s", d)
	}
	if _, err := boot(dhcpclient.RandomMAC()); err == nil {
		t.Fatal("dynamic BOOTP answered although not enabled on the scope")
	}
}

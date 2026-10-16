//go:build integration && linux

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/miekg/dns"

	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/test/dhcpclient"
	"github.com/meister119/leasorix/test/netns"
)

// Test-only TSIG key of test/dns (named.conf, pdns/init.sh).
const testTSIGSecret = "bGVhc29yaXgtaW50ZWdyYXRpb24tdGVzdC1rZXktMDE="

const (
	bindAddr = "10.78.0.53"
	pdnsAddr = "10.78.0.54"
)

// dnsServers starts BIND and PowerDNS (test/dns/compose.yml) on a macvlan
// network attached to the server namespace through interface d0
// (10.78.0.1/24). Images can be overridden with LEASORIX_TEST_BIND_IMAGE
// and LEASORIX_TEST_PDNS_IMAGE (e.g. for a registry mirror).
func dnsServers(t *testing.T, tp *topo) {
	t.Helper()
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available:", err)
	}
	sfx := netns.Suffix()
	parent, peer := "lxdn"+sfx, "lxdq"+sfx
	for _, args := range [][]string{
		{"link", "add", parent, "type", "veth", "peer", "name", peer},
		{"link", "set", peer, "netns", tp.srv.Name},
		{"link", "set", parent, "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v %s", args, err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", parent).Run() })
	if err := tp.srv.Sh(fmt.Sprintf("ip link set %s name d0 && ip addr add 10.78.0.1/24 dev d0 && ip link set d0 up", peer)); err != nil {
		t.Fatal(err)
	}
	project := "lxdns" + sfx
	compose := func(args ...string) *exec.Cmd {
		cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", filepath.Join("..", "dns", "compose.yml")}, args...)...)
		cmd.Env = append(os.Environ(), "DNS_PARENT="+parent)
		if v := os.Getenv("LEASORIX_TEST_BIND_IMAGE"); v != "" {
			cmd.Env = append(cmd.Env, "BIND_IMAGE="+v)
		}
		if v := os.Getenv("LEASORIX_TEST_PDNS_IMAGE"); v != "" {
			cmd.Env = append(cmd.Env, "PDNS_IMAGE="+v)
		}
		return cmd
	}
	if out, err := compose("up", "-d", "--quiet-pull").CombinedOutput(); err != nil {
		t.Fatalf("starting DNS servers: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := compose("logs", "--tail", "40").CombinedOutput()
			t.Logf("DNS server logs:\n%s", out)
		}
		_ = compose("down", "-v").Run()
	})
	for _, srv := range []struct{ addr, zone string }{{bindAddr, "bind.test"}, {pdnsAddr, "pdns.test"}} {
		waitFor(t, 60*time.Second, "DNS server "+srv.addr, func() bool {
			return len(lookup(tp, srv.addr, srv.zone, dns.TypeSOA)) > 0
		})
	}
}

// lookup queries a DNS server from the server namespace.
func lookup(tp *topo, server, name string, qtype uint16) []dns.RR {
	var answer []dns.RR
	_ = tp.srv.Do(func() error {
		c := &dns.Client{Timeout: 2 * time.Second}
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qtype)
		r, _, err := c.Exchange(m, net.JoinHostPort(server, "53"))
		if err == nil {
			answer = r.Answer
		}
		return nil
	})
	return answer
}

func aRecord(tp *topo, server, name string) string {
	for _, rr := range lookup(tp, server, name, dns.TypeA) {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String()
		}
	}
	return ""
}

func ptrRecord(tp *topo, server, ip string) string {
	rev, _ := dns.ReverseAddr(ip)
	for _, rr := range lookup(tp, server, rev, dns.TypePTR) {
		if p, ok := rr.(*dns.PTR); ok {
			return strings.TrimSuffix(p.Ptr, ".")
		}
	}
	return ""
}

func hasDHCID(tp *topo, server, name string) bool {
	return len(lookup(tp, server, name, dns.TypeDHCID)) > 0
}

// fqdnOption is a Client FQDN option (81) in wire format with the S bit.
func fqdnOption(label string) dhcpv4.Option {
	return dhcpv4.OptGeneric(dhcpv4.OptionFQDN, append([]byte{0x05, 0, 0, byte(len(label))}, append([]byte(label), 0)...))
}

// TestDNSUpdates registers leases in BIND (mode "client", Client FQDN
// option) and PowerDNS (mode "always", host name only) with TSIG, checks
// name protection against manual records and other clients, and the
// removal of the records on release and on expiry.
func TestDNSUpdates(t *testing.T) {
	tp := newTopo(t)
	dnsServers(t, tp)
	// Second client link for the PowerDNS scope.
	if err := netns.Veth(tp.srv, "s1", tp.cli, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := tp.srv.Sh("ip addr add 10.76.0.1/16 dev s1"); err != nil {
		t.Fatal(err)
	}
	tp.ifaces = append(tp.ifaces, "s1")
	tp.seed(func(ctx context.Context, s *scope.Store) {
		key, err := s.CreateDNSKey(ctx, scope.DNSKey{Name: "leasorix-key", Secret: testTSIGSecret})
		if err != nil {
			t.Fatal(err)
		}
		for _, z := range []scope.DNSZone{
			{Name: "bind.test", Server: bindAddr, KeyID: key.ID},
			{Name: "77.10.in-addr.arpa", Server: bindAddr, KeyID: key.ID},
			{Name: "pdns.test", Server: pdnsAddr, KeyID: key.ID},
			{Name: "76.10.in-addr.arpa", Server: pdnsAddr + ":53", KeyID: key.ID},
		} {
			if _, err := s.CreateDNSZone(ctx, z); err != nil {
				t.Fatal(err)
			}
		}
		b := mustScope(t, s, "bind", "10.77.0.0/16", "10.77.1.10", "10.77.1.200", 40*time.Second)
		b.DNS = scope.DNSSettings{Mode: scope.DNSClient, Domain: "bind.test", Cleanup: true, Protect: true}
		p := mustScope(t, s, "pdns", "10.76.0.0/16", "10.76.1.10", "10.76.1.200", time.Hour)
		p.DNS = scope.DNSSettings{Mode: scope.DNSAlways, Domain: "pdns.test", Cleanup: true, Protect: true}
		for _, sc := range []scope.Scope{b, p} {
			if _, err := s.UpdateScope(ctx, sc); err != nil {
				t.Fatal(err)
			}
		}
	})
	tp.startServer("")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Run("bind", func(t *testing.T) {
		conn := tp.conn()
		a := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Options: []dhcpv4.Option{fqdnOption("Host-A")}}
		ackA, err := a.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ipA := ackA.YourIPAddr.String()
		if f := ackA.Options.Get(dhcpv4.OptionFQDN); len(f) < 3 || f[0]&0x01 == 0 || !strings.Contains(string(f), "bind") {
			t.Fatalf("ACK option 81 = %x", f)
		}
		waitFor(t, 20*time.Second, "A/PTR of host-a in BIND", func() bool {
			return aRecord(tp, bindAddr, "host-a.bind.test") == ipA && ptrRecord(tp, bindAddr, ipA) == "host-a.bind.test"
		})
		if !hasDHCID(tp, bindAddr, "host-a.bind.test") {
			t.Fatal("no DHCID record (name protection)")
		}

		// Name protection: a manually created name and a name of another
		// client are not overwritten; the PTR of the new address is set.
		b := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Options: []dhcpv4.Option{fqdnOption("static")}}
		ackB, err := b.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Options: []dhcpv4.Option{fqdnOption("host-a")}}
		ackC, err := c.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ipB, ipC := ackB.YourIPAddr.String(), ackC.YourIPAddr.String()
		waitFor(t, 20*time.Second, "PTR records of the conflicting clients", func() bool {
			return ptrRecord(tp, bindAddr, ipB) == "static.bind.test" && ptrRecord(tp, bindAddr, ipC) == "host-a.bind.test"
		})
		if got := aRecord(tp, bindAddr, "static.bind.test"); got != "10.78.9.9" {
			t.Fatalf("manual record overwritten: %s", got)
		}
		if got := aRecord(tp, bindAddr, "host-a.bind.test"); got != ipA {
			t.Fatalf("name of another client overwritten: %s", got)
		}

		// Release: records of client A are removed.
		if err := a.Release(ctx, ackA); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 40*time.Second, "removal after release", func() bool {
			return aRecord(tp, bindAddr, "host-a.bind.test") == "" && ptrRecord(tp, bindAddr, ipA) == "" && !hasDHCID(tp, bindAddr, "host-a.bind.test")
		})
		// Expiry: client C does not renew; its PTR goes away.
		waitFor(t, 90*time.Second, "removal after expiry", func() bool {
			return ptrRecord(tp, bindAddr, ipC) == ""
		})
	})

	t.Run("powerdns", func(t *testing.T) {
		var conn *dhcpclient.Conn
		if err := tp.cli.Do(func() error {
			var err error
			conn, err = dhcpclient.Listen("c1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// Mode "always": the host name option is enough.
		p := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Hostname: "Printer"}
		ack, err := p.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ip := ack.YourIPAddr.String()
		waitFor(t, 20*time.Second, "A/PTR of printer in PowerDNS", func() bool {
			return aRecord(tp, pdnsAddr, "printer.pdns.test") == ip && ptrRecord(tp, pdnsAddr, ip) == "printer.pdns.test"
		})
		if !hasDHCID(tp, pdnsAddr, "printer.pdns.test") {
			t.Fatal("no DHCID record")
		}
		s := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Hostname: "static"}
		ackS, err := s.DORA(ctx)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 20*time.Second, "PTR of the conflicting client", func() bool {
			return ptrRecord(tp, pdnsAddr, ackS.YourIPAddr.String()) == "static.pdns.test"
		})
		if got := aRecord(tp, pdnsAddr, "static.pdns.test"); got != "10.78.9.9" {
			t.Fatalf("manual record overwritten: %s", got)
		}
		if err := p.Release(ctx, ack); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 40*time.Second, "removal after release", func() bool {
			return aRecord(tp, pdnsAddr, "printer.pdns.test") == "" && ptrRecord(tp, pdnsAddr, ip) == "" && !hasDHCID(tp, pdnsAddr, "printer.pdns.test")
		})
	})

	// The registrations and conflicts are visible to the administrator.
	waitFor(t, 10*time.Second, "conflict audit entries", func() bool {
		n := 0
		for _, l := range readLines(tp.logPath) {
			if strings.Contains(l, "name protection") {
				n++
			}
		}
		return n >= 3
	})
}

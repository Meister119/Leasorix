//go:build integration && linux

package integration

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/test/dhcpclient"
	"github.com/meister119/leasorix/test/netns"
)

// TestDhclientLifecycle: obtain, renew (unicast RENEWING), INIT-REBOOT and
// release with ISC dhclient.
func TestDhclientLifecycle(t *testing.T) {
	dhclient := dhclientBin(t)
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", 20*time.Second)
	})
	tp.startServer("")

	logPath := filepath.Join(tp.dir, "dhclient.events")
	script := filepath.Join(tp.dir, "dhclient-script")
	writeScript(t, script, fmt.Sprintf(`echo "$reason $new_ip_address $new_dhcp_lease_time" >> %s
case "$reason" in
  BOUND|RENEW|REBIND|REBOOT) ip addr flush dev "$interface"; ip addr add "$new_ip_address/$new_subnet_mask" dev "$interface" ;;
  EXPIRE|FAIL|RELEASE|STOP) ip addr flush dev "$interface" ;;
esac
exit 0
`, logPath))
	lf, pf := filepath.Join(tp.dir, "dhclient.leases"), filepath.Join(tp.dir, "dhclient.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := tp.cli.Command(ctx, dhclient, "-4", "-d", "-sf", script, "-lf", lf, "-pf", pf, "c0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()

	var ip string
	waitFor(t, 20*time.Second, "dhclient BOUND", func() bool {
		for _, l := range readLines(logPath) {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "BOUND" {
				ip = f[1]
				return true
			}
		}
		return false
	})
	if ip != "10.77.0.100" {
		t.Fatalf("dhclient bound to %s", ip)
	}
	first, _ := tp.leaseFor(ip)
	if first.State != "active" {
		t.Fatalf("lease not persisted active: %+v", first)
	}
	// T1 is 10s: wait for a unicast renewal.
	waitFor(t, 25*time.Second, "dhclient RENEW", func() bool {
		for _, l := range readLines(logPath) {
			if strings.HasPrefix(l, "RENEW ") {
				return true
			}
		}
		return false
	})
	renewed, _ := tp.leaseFor(ip)
	if renewed.Expires <= first.Expires {
		t.Fatalf("renewal did not extend the lease: %d -> %d", first.Expires, renewed.Expires)
	}
	cancel()
	_ = cmd.Wait()

	// Restart: dhclient reuses its lease file -> INIT-REBOOT.
	out, err := tp.cli.Output(dhclient, "-4", "-1", "-v", "-sf", script, "-lf", lf, "-pf", pf, "c0")
	if err != nil {
		t.Fatalf("dhclient restart: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DHCPACK of "+ip) {
		t.Fatalf("INIT-REBOOT did not get the same address:\n%s", out)
	}
	// Release.
	out, err = tp.cli.Output(dhclient, "-4", "-r", "-v", "-sf", script, "-lf", lf, "-pf", pf, "c0")
	if err != nil {
		t.Fatalf("dhclient -r: %v\n%s", err, out)
	}
	waitFor(t, 5*time.Second, "lease released", func() bool {
		l, _ := tp.leaseFor(ip)
		return l.State == "released"
	})
}

// TestUdhcpc obtains and releases a lease with busybox udhcpc.
func TestUdhcpc(t *testing.T) {
	if _, err := exec.LookPath("udhcpc"); err != nil {
		t.Skip("udhcpc not installed")
	}
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
	})
	tp.startServer("")
	events := filepath.Join(tp.dir, "udhcpc.events")
	script := filepath.Join(tp.dir, "udhcpc-script")
	// Like the stock script: configure the address (udhcpc sends RELEASE
	// from the leased address).
	writeScript(t, script, fmt.Sprintf(`echo "$1 $ip $mask $lease $serverid" >> %s
case "$1" in
  bound|renew) ip addr flush dev "$interface"; ip addr add "$ip/$mask" dev "$interface" ;;
  deconfig) ip addr flush dev "$interface" ;;
esac
exit 0
`, events))
	// Without -q udhcpc stays BOUND; SIGTERM + -R then sends a RELEASE
	// (with -q busybox exits before entering BOUND and never releases).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := tp.cli.Command(ctx, "udhcpc", "-i", "c0", "-f", "-n", "-t", "5", "-T", "1", "-R",
		"-s", script, "-x", "hostname:udtest")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var bound []string
	waitFor(t, 15*time.Second, "udhcpc bound", func() bool {
		for _, l := range readLines(events) {
			if strings.HasPrefix(l, "bound ") {
				bound = strings.Fields(l)
				return true
			}
		}
		return false
	})
	if len(bound) < 5 || bound[1] != "10.77.0.100" || bound[2] != "16" || bound[3] != "3600" || bound[4] != "10.77.0.1" {
		t.Fatalf("unexpected bound event %v", bound)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	// udhcpc exits right after sending RELEASE.
	waitFor(t, 5*time.Second, "lease released", func() bool {
		l, _ := tp.leaseFor("10.77.0.100")
		return l.State == "released"
	})
	l, _ := tp.leaseFor("10.77.0.100")
	if l.ClientID == "" {
		t.Errorf("client identifier not stored: %+v", l)
	}
	if l.Hostname != "udtest" {
		t.Errorf("hostname = %q", l.Hostname)
	}
}

// TestGoClientScenarios covers reservations, decline, inform, NAK and
// restart persistence with the Go test client.
func TestGoClientScenarios(t *testing.T) {
	tp := newTopo(t)
	reservedMAC := net.HardwareAddr{0x02, 0xaa, 0, 0, 0, 0x01}
	tp.seed(func(ctx context.Context, s *scope.Store) {
		sc := mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
		if _, err := s.AddExclusion(ctx, scope.Exclusion{ScopeID: sc.ID, Start: netip.MustParseAddr("10.77.0.100"), End: netip.MustParseAddr("10.77.0.104")}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateReservation(ctx, scope.Reservation{ScopeID: sc.ID, IP: netip.MustParseAddr("10.77.0.50"), MAC: reservedMAC.String(), Hostname: "printer"}); err != nil {
			t.Fatal(err)
		}
	})
	tp.startServer("")
	conn := tp.conn()
	ctx := context.Background()

	// Exclusions are honoured.
	a := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Hostname: "client-a"}
	ack, err := a.DORA(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := ack.YourIPAddr.String(); got != "10.77.0.105" {
		t.Fatalf("first dynamic address = %s", got)
	}

	// Reservation is preferred.
	r := &dhcpclient.Client{Conn: conn, MAC: reservedMAC}
	ack, err = r.DORA(ctx)
	if err != nil || ack.YourIPAddr.String() != "10.77.0.50" {
		t.Fatalf("reservation: %v %v", ack, err)
	}
	if l, _ := tp.leaseFor("10.77.0.50"); l.Hostname != "printer" {
		t.Errorf("reservation hostname not used: %+v", l)
	}

	// Decline blocks the address.
	d := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	ack, err = d.DORA(ctx)
	if err != nil {
		t.Fatal(err)
	}
	declined := ack.YourIPAddr.String()
	if err := d.Decline(ctx, ack); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "declined state", func() bool {
		l, _ := tp.leaseFor(declined)
		return l.State == "declined"
	})
	ack, err = d.DORA(ctx)
	if err != nil || ack.YourIPAddr.String() == declined {
		t.Fatalf("declined address handed out again: %v %v", ack, err)
	}

	// Release.
	if err := d.Release(ctx, ack); err != nil {
		t.Fatal(err)
	}
	released := ack.YourIPAddr.String()
	waitFor(t, 5*time.Second, "released state", func() bool {
		l, _ := tp.leaseFor(released)
		return l.State == "released"
	})

	// INIT-REBOOT for a foreign address is NAKed.
	x := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	nak, err := x.RequestAddress(ctx, net.ParseIP("10.77.0.105"))
	if err != nil || nak.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("expected NAK, got %v %v", nak, err)
	}
	// ... and for an address of another network.
	nak, err = x.RequestAddress(ctx, net.ParseIP("192.168.99.9"))
	if err != nil || nak.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("expected NAK for wrong network, got %v %v", nak, err)
	}

	// INFORM from a statically configured host (needs the address locally
	// so the unicast reply is accepted).
	if err := tp.cli.Sh("ip addr add 10.77.5.5/16 dev c0"); err != nil {
		t.Fatal(err)
	}
	inf, err := x.Inform(ctx, net.ParseIP("10.77.5.5"))
	if err != nil {
		t.Fatalf("inform: %v", err)
	}
	if inf.Options.Has(dhcpv4.OptionIPAddressLeaseTime) || inf.SubnetMask().String() != "ffff0000" {
		t.Fatalf("inform reply: %s", inf.Summary())
	}
	if err := tp.cli.Sh("ip addr del 10.77.5.5/16 dev c0"); err != nil {
		t.Fatal(err)
	}

	// Restart: leases survive, client a gets its address back via
	// INIT-REBOOT.
	tp.stopServer()
	tp.startServer("")
	ack, err = a.RequestAddress(ctx, net.ParseIP("10.77.0.105"))
	if err != nil || ack.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("after restart: %v %v", ack, err)
	}
}

// TestConflictDetection: an address already used by a static host is
// skipped and blocked.
func TestConflictDetection(t *testing.T) {
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
	})
	tp.startServer("  conflict_detection: {enabled: true, timeout: 300ms, attempts: 1}\n")
	// A "static" host using the first pool address.
	if err := tp.cli.Sh("ip addr add 10.77.0.100/16 dev c0"); err != nil {
		t.Fatal(err)
	}
	conn := tp.conn()
	c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	ack, err := c.DORA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ack.YourIPAddr.String() != "10.77.0.101" {
		t.Fatalf("conflicting address not skipped: got %s", ack.YourIPAddr)
	}
	if l, ok := tp.leaseFor("10.77.0.100"); !ok || l.State != "declined" {
		t.Fatalf("conflict not recorded: %+v", l)
	}
}

// TestRelay: client behind a dnsmasq DHCP relay in another subnet.
func TestRelay(t *testing.T) {
	if _, err := exec.LookPath("dnsmasq"); err != nil {
		t.Skip("dnsmasq not installed")
	}
	if _, err := exec.LookPath("udhcpc"); err != nil {
		t.Skip("udhcpc not installed")
	}
	tp := newTopo(t)
	sfx := netns.Suffix()
	rly, err := netns.New("lxr" + sfx)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := netns.New("lxrc" + sfx)
	if err != nil {
		_ = rly.Delete()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Delete(); _ = rly.Delete() })
	// srv/s1 10.66.0.1 <-> rly/r0 10.66.0.2 ; rly/r1 10.88.0.1 <-> rc/rc0
	if err := netns.Veth(tp.srv, "s1", rly, "r0"); err != nil {
		t.Fatal(err)
	}
	if err := netns.Veth(rly, "r1", rc, "rc0"); err != nil {
		t.Fatal(err)
	}
	for ns, sh := range map[*netns.NS]string{
		tp.srv: "ip addr add 10.66.0.1/24 dev s1 && ip route add 10.88.0.0/24 via 10.66.0.2",
		rly:    "ip addr add 10.66.0.2/24 dev r0 && ip addr add 10.88.0.1/24 dev r1 && sysctl -qw net.ipv4.ip_forward=1",
	} {
		if err := ns.Sh(sh); err != nil {
			t.Fatal(err)
		}
	}
	var remoteID int64
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
		remoteID = mustScope(t, s, "remote", "10.88.0.0/24", "10.88.0.50", "10.88.0.60", time.Hour).ID
	})
	tp.ifaces = []string{"s0", "s1"}
	tp.startServer("")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := rly.Command(ctx, "dnsmasq", "--no-daemon", "--port=0", "--dhcp-relay=10.88.0.1,10.66.0.1",
		"--interface=r1", "--bind-interfaces", "--pid-file="+filepath.Join(tp.dir, "dnsmasq.pid"))
	if err := relay.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = relay.Wait() }()
	time.Sleep(300 * time.Millisecond)

	events := filepath.Join(tp.dir, "udhcpc.events")
	script := filepath.Join(tp.dir, "udhcpc-script")
	writeScript(t, script, fmt.Sprintf("echo \"$1 $ip $serverid\" >> %s\nexit 0\n", events))
	out, err := rc.Output("udhcpc", "-i", "rc0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", script)
	if err != nil {
		t.Fatalf("udhcpc via relay: %v\n%s", err, out)
	}
	l, ok := tp.leaseFor("10.88.0.50")
	if !ok || l.ScopeID != remoteID || l.State != "active" {
		t.Fatalf("relayed lease: %+v\n%s", l, out)
	}
	if !strings.Contains(out, "obtained from 10.66.0.1") {
		t.Fatalf("server identifier is not the address facing the relay:\n%s", out)
	}
}

// TestParallelClientsNoDuplicates: 1000 concurrent clients, every one gets a
// unique address and every lease is persisted.
func TestParallelClientsNoDuplicates(t *testing.T) {
	tp := newTopo(t)
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.1.0", "10.77.8.255", time.Hour)
	})
	tp.startServer("")
	conn := tp.conn()
	const n = 1000
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	start := time.Now()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ips  = map[string]string{}
		errs []error
	)
	sem := make(chan struct{}, 250)
	for range n {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
			ack, err := c.DORA(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ip := ack.YourIPAddr.String()
			if prev, dup := ips[ip]; dup {
				errs = append(errs, fmt.Errorf("duplicate %s for %s and %s", ip, prev, c.MAC))
			}
			ips[ip] = c.MAC.String()
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	if len(errs) > 0 {
		t.Fatalf("%d errors, first: %v", len(errs), errs[0])
	}
	if len(ips) != n {
		t.Fatalf("got %d unique addresses, want %d", len(ips), n)
	}
	active := 0
	seen := map[string]bool{}
	for _, l := range tp.leases() {
		if l.State == "active" {
			active++
			if seen[l.IP] {
				t.Fatalf("duplicate lease row %s", l.IP)
			}
			seen[l.IP] = true
		}
	}
	if active != n {
		t.Fatalf("persisted active leases = %d, want %d", active, n)
	}
	t.Logf("%d clients completed DORA in %s (%.0f leases/s)", n, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds())
}

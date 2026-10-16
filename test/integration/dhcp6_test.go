//go:build integration && linux

package integration

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/scope6"
	"github.com/meister119/leasorix/internal/store"
)

// setupIPv6 enables IPv6 without duplicate address detection delays on
// both ends of the link and gives the server a global address.
func setupIPv6(t *testing.T, tp *topo) {
	t.Helper()
	for _, ns := range []struct {
		sh  func(string) error
		dev string
	}{{tp.srv.Sh, "s0"}, {tp.cli.Sh, "c0"}} {
		script := "sysctl -qw net.ipv6.conf.all.disable_ipv6=0 net.ipv6.conf.all.accept_dad=0 net.ipv6.conf.default.accept_dad=0 net.ipv6.conf." + ns.dev + ".accept_dad=0 && " +
			"ip link set " + ns.dev + " down && ip link set " + ns.dev + " up"
		if err := ns.sh(script); err != nil {
			t.Fatal(err)
		}
	}
	if err := tp.srv.Sh("ip -6 addr add 2001:db8:77::1/64 dev s0 nodad"); err != nil {
		t.Fatal(err)
	}
	// Wait for the link-local addresses.
	waitFor(t, 10*time.Second, "link-local addresses", func() bool {
		a, _ := tp.srv.Output("ip", "-6", "addr", "show", "dev", "s0", "scope", "link")
		b, _ := tp.cli.Output("ip", "-6", "addr", "show", "dev", "c0", "scope", "link")
		return strings.Contains(a, "fe80") && strings.Contains(b, "fe80") && !strings.Contains(a+b, "tentative")
	})
}

type lease6Row struct {
	IP, Kind, DUID, State string
	Bits                  int
	ScopeID               int64
}

func (tp *topo) leases6() []lease6Row {
	tp.t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, tp.dbPath)
	if err != nil {
		tp.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.R.QueryContext(ctx, `SELECT ip, prefix_len, kind, duid, state, scope_id FROM leases6 ORDER BY ip`)
	if err != nil {
		tp.t.Fatal(err)
	}
	defer rows.Close()
	var out []lease6Row
	for rows.Next() {
		var r lease6Row
		if err := rows.Scan(&r.IP, &r.Bits, &r.Kind, &r.DUID, &r.State, &r.ScopeID); err != nil {
			tp.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (tp *topo) lease6(ip string) (lease6Row, bool) {
	for _, l := range tp.leases6() {
		if l.IP == ip {
			return l, true
		}
	}
	return lease6Row{}, false
}

// TestDHCPv6 runs stateful DHCPv6 against dhclient -6 (addresses and
// prefix delegation, release), busybox udhcpc6, the library client
// (renew, information request) and a relayed request.
func TestDHCPv6(t *testing.T) {
	if _, err := os.Stat("/proc/net/if_inet6"); err != nil {
		t.Skip("IPv6 is disabled in this kernel")
	}
	tp := newTopo(t)
	setupIPv6(t, tp)
	var lan, remote scope6.Scope
	tp.seed(func(ctx context.Context, _ *scope.Store) {})
	{
		ctx := context.Background()
		db, err := store.Open(ctx, tp.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		s := scope6.NewStore(db)
		if err := s.Load(ctx); err != nil {
			t.Fatal(err)
		}
		lan, err = s.CreateScope(ctx, scope6.Scope{
			Name: "lan6", Prefix: netip.MustParsePrefix("2001:db8:77::/64"),
			Start: netip.MustParseAddr("2001:db8:77::100"), End: netip.MustParseAddr("2001:db8:77::1ff"),
			PreferredLifetime: 30 * time.Minute, ValidLifetime: time.Hour, Enabled: true,
			PDPool: netip.MustParsePrefix("2001:db8:7700::/40"), PDLength: 56,
		})
		if err != nil {
			t.Fatal(err)
		}
		remote, err = s.CreateScope(ctx, scope6.Scope{
			Name: "remote6", Prefix: netip.MustParsePrefix("2001:db8:78::/64"),
			Start: netip.MustParseAddr("2001:db8:78::10"), End: netip.MustParseAddr("2001:db8:78::20"),
			PreferredLifetime: 30 * time.Minute, ValidLifetime: time.Hour, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetOption(ctx, scope6.OptionValue{Owner: scope6.ServerOwner, Code: 23, Value: "2001:db8::53"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetOption(ctx, scope6.OptionValue{Owner: scope6.Owner{Kind: scope6.OwnerScope, ID: lan.ID}, Code: 24, Value: "lan6.example"}); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	tp.startServer("dhcp6:\n  interfaces: [s0]\n  expiry_interval: 1s\n")
	waitFor(t, 15*time.Second, "dhcpv6 listener", func() bool {
		return strings.Contains(strings.Join(readLines(tp.logPath), "\n"), "dhcpv6 listening")
	})

	t.Run("dhclient", func(t *testing.T) {
		if _, err := exec.LookPath("dhclient"); err != nil {
			t.Skip("dhclient not installed")
		}
		dir := t.TempDir()
		lf, pf := filepath.Join(dir, "lease6"), filepath.Join(dir, "pid")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cmd := tp.cli.Command(ctx, "dhclient", "-6", "-d", "-N", "-P", "-sf", "/bin/true", "-lf", lf, "-pf", pf, "c0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { cancel(); _ = cmd.Wait() }()
		var lease, addr, prefix string
		waitFor(t, 30*time.Second, "dhclient -6 lease", func() bool {
			b, _ := os.ReadFile(lf)
			lease = string(b)
			addr, prefix = field(lease, "iaaddr "), field(lease, "iaprefix ")
			return addr != "" && prefix != ""
		})
		if !strings.HasPrefix(addr, "2001:db8:77::1") || !strings.HasPrefix(prefix, "2001:db8:77") || !strings.HasSuffix(prefix, "/56") {
			t.Fatalf("lease file:\n%s", lease)
		}
		if !strings.Contains(lease, "2001:db8::53") {
			t.Fatalf("DNS server option missing in lease file:\n%s", lease)
		}
		l, ok := tp.lease6(addr)
		if !ok || l.State != "active" || l.Kind != "na" || l.ScopeID != lan.ID {
			t.Fatalf("server binding of %s: %+v", addr, l)
		}
		p, ok := tp.lease6(strings.TrimSuffix(prefix, "/56"))
		if !ok || p.Kind != "pd" || p.Bits != 56 {
			t.Fatalf("server binding of %s: %+v", prefix, p)
		}
		// Release (dhclient -r also stops the running client).
		rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer rcancel()
		if out, err := tp.cli.Command(rctx, "dhclient", "-6", "-r", "-N", "-P", "-sf", "/bin/true", "-lf", lf, "-pf", pf, "c0").CombinedOutput(); err != nil {
			t.Fatalf("dhclient -6 -r: %v\n%s", err, out)
		}
		waitFor(t, 10*time.Second, "release", func() bool {
			l, _ := tp.lease6(addr)
			p, _ := tp.lease6(strings.TrimSuffix(prefix, "/56"))
			return l.State == "released" && p.State == "released"
		})
	})

	t.Run("udhcpc6", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "env")
		writeScript(t, filepath.Join(dir, "script"), `[ "$1" = bound ] && echo "ipv6=$ipv6 prefix=$ipv6prefix dns=$dns" >> `+out+"\nexit 0\n")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if o, err := tp.cli.Command(ctx, "busybox", "udhcpc6", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-d", "-s", filepath.Join(dir, "script")).CombinedOutput(); err != nil {
			t.Fatalf("udhcpc6: %v\n%s", err, o)
		}
		b, _ := os.ReadFile(out)
		got := string(b)
		if !strings.Contains(got, "ipv6=2001:db8:77::") || !strings.Contains(got, "prefix=2001:db8:77") || !strings.Contains(got, "dns=2001:db8::53") {
			t.Fatalf("udhcpc6 bound with %q", got)
		}
	})

	t.Run("go client: renew and information request", func(t *testing.T) {
		var c *nclient6.Client
		if err := tp.cli.Do(func() error {
			var err error
			c, err = nclient6.New("c0", nclient6.WithTimeout(2*time.Second), nclient6.WithRetry(3))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		adv, err := c.Solicit(ctx, dhcpv6.WithIAID([4]byte{0, 0, 0, 1}))
		if err != nil {
			t.Fatal(err)
		}
		rep, err := c.Request(ctx, adv)
		if err != nil {
			t.Fatal(err)
		}
		addr := rep.Options.OneIANA().Options.OneAddress()
		if addr == nil || !strings.HasPrefix(addr.IPv6Addr.String(), "2001:db8:77::1") || addr.ValidLifetime != time.Hour {
			t.Fatalf("reply %s", rep.Summary())
		}
		// RENEW keeps the address.
		renew := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeRenew}
		renew.TransactionID, _ = dhcpv6.GenerateTransactionID()
		renew.AddOption(rep.GetOneOption(dhcpv6.OptionClientID))
		renew.AddOption(rep.GetOneOption(dhcpv6.OptionServerID))
		renew.AddOption(&dhcpv6.OptIANA{IaId: [4]byte{0, 0, 0, 1}, Options: dhcpv6.IdentityOptions{Options: []dhcpv6.Option{&dhcpv6.OptIAAddress{IPv6Addr: addr.IPv6Addr}}}})
		got, err := c.SendAndRead(ctx, c.RemoteAddr(), renew, nclient6.IsMessageType(dhcpv6.MessageTypeReply))
		if err != nil {
			t.Fatal(err)
		}
		if a := got.Options.OneIANA().Options.OneAddress(); a == nil || !a.IPv6Addr.Equal(addr.IPv6Addr) || a.ValidLifetime != time.Hour {
			t.Fatalf("renew %s", got.Summary())
		}
		// INFORMATION-REQUEST (stateless).
		info := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeInformationRequest}
		info.TransactionID, _ = dhcpv6.GenerateTransactionID()
		info.AddOption(dhcpv6.OptRequestedOption(dhcpv6.OptionDNSRecursiveNameServer))
		got, err = c.SendAndRead(ctx, c.RemoteAddr(), info, nclient6.IsMessageType(dhcpv6.MessageTypeReply))
		if err != nil {
			t.Fatal(err)
		}
		if dns := got.Options.DNS(); len(dns) != 1 || dns[0].String() != "2001:db8::53" {
			t.Fatalf("information request %s", got.Summary())
		}
	})

	t.Run("relay", func(t *testing.T) {
		if err := tp.cli.Sh("ip -6 addr add 2001:db8:77::fe/64 dev c0 nodad"); err != nil {
			t.Fatal(err)
		}
		var conn net.PacketConn
		if err := tp.cli.Do(func() error {
			var err error
			conn, err = net.ListenPacket("udp6", "[2001:db8:77::fe]:5470")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		sol := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeSolicit, TransactionID: dhcpv6.TransactionID{9, 9, 9}}
		sol.AddOption(dhcpv6.OptClientID(&dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{2, 0, 0, 0, 0x78, 1}}))
		sol.AddOption(&dhcpv6.OptIANA{IaId: [4]byte{0, 0, 0, 1}})
		fwd, err := dhcpv6.EncapsulateRelay(sol, dhcpv6.MessageTypeRelayForward, net.ParseIP("2001:db8:78::1"), net.ParseIP("fe80::78"))
		if err != nil {
			t.Fatal(err)
		}
		fwd.AddOption(dhcpv6.OptInterfaceID([]byte("vlan78")))
		if _, err := conn.WriteTo(fwd.ToBytes(), &net.UDPAddr{IP: net.ParseIP("2001:db8:77::1"), Port: 547}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("no relay reply: %v", err)
		}
		m, err := dhcpv6.FromBytes(buf[:n])
		if err != nil || m.Type() != dhcpv6.MessageTypeRelayReply {
			t.Fatalf("relay reply %v %v", m, err)
		}
		if string(m.(*dhcpv6.RelayMessage).Options.InterfaceID()) != "vlan78" {
			t.Fatal("interface id not echoed")
		}
		inner, _ := m.GetInnerMessage()
		a := inner.Options.OneIANA().Options.OneAddress()
		if inner.Type() != dhcpv6.MessageTypeAdvertise || a == nil || !netip.MustParsePrefix("2001:db8:78::/64").Contains(addrOf6(a.IPv6Addr)) {
			t.Fatalf("relayed advertise %s", inner.Summary())
		}
		_ = remote
	})
}

func addrOf6(ip net.IP) netip.Addr {
	a, _ := netip.AddrFromSlice(ip.To16())
	return a
}

// field returns the first token after key in a dhclient lease file.
func field(s, key string) string {
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := strings.Fields(s[i+len(key):])
	if len(rest) == 0 {
		return ""
	}
	return strings.TrimSuffix(rest[0], "{")
}

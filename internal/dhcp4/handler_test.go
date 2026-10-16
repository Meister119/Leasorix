package dhcp4

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

type henv struct {
	t      testing.TB
	scopes *scope.Store
	leases *lease.Manager
	h      *Handler
	ifAddr []netip.Prefix
}

func newHEnv(t testing.TB) *henv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	e := &henv{t: t, scopes: scope.NewStore(db), ifAddr: []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")}}
	if err := e.scopes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	e.leases = lease.NewManager(lease.DefaultConfig(), log, lease.NewDBStore(db), nil)
	e.scopes.OnChange(e.leases.ScopesChanged)
	done := make(chan struct{})
	go func() { _ = e.leases.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = db.Close() })
	e.h = NewHandler(log, e.scopes, e.leases)
	return e
}

func (e *henv) addScope(name, cidr, start, end string, leaseTime time.Duration) *scope.Pool {
	e.t.Helper()
	sc, err := e.scopes.CreateScope(context.Background(), scope.Scope{
		Name: name, Subnet: netip.MustParsePrefix(cidr), Start: netip.MustParseAddr(start),
		End: netip.MustParseAddr(end), LeaseTime: leaseTime, Enabled: true,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.scopes.Snapshot().ByID(sc.ID)
}

var testMAC = net.HardwareAddr{0x02, 0, 0, 0, 0, 0x01}

func msg(t testing.TB, mt dhcpv4.MessageType, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	m, err := dhcpv4.New(append([]dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithHwAddr(testMAC),
	}, mods...)...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *henv) handle(m *dhcpv4.DHCPv4, dst string) *Response {
	e.t.Helper()
	return e.h.Handle(context.Background(), &Request{
		Msg: m, IfName: "eth0", IfAddrs: e.ifAddr,
		Src: netip.MustParseAddrPort("0.0.0.0:68"), Dst: netip.MustParseAddr(dst),
	})
}

func mustIP(s string) net.IP { return net.ParseIP(s).To4() }

func (e *henv) dora() (*Response, *Response) {
	e.t.Helper()
	offer := e.handle(msg(e.t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true)), "255.255.255.255")
	if offer == nil {
		e.t.Fatal("no offer")
	}
	ack := e.handle(msg(e.t, dhcpv4.MessageTypeRequest, dhcpv4.WithBroadcast(true),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(offer.Msg.ServerIdentifier())),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offer.Msg.YourIPAddr))), "255.255.255.255")
	if ack == nil || ack.Msg.MessageType() != dhcpv4.MessageTypeAck {
		e.t.Fatalf("no ack: %v", ack)
	}
	return offer, ack
}

func TestDiscoverOfferOptionsAndAddressing(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)

	r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true)), "255.255.255.255")
	if r == nil {
		t.Fatal("no offer")
	}
	m := r.Msg
	if m.MessageType() != dhcpv4.MessageTypeOffer || !m.YourIPAddr.Equal(mustIP("10.0.0.100")) {
		t.Fatalf("unexpected offer %s", m.Summary())
	}
	if !m.ServerIdentifier().Equal(mustIP("10.0.0.1")) {
		t.Fatalf("server id = %s", m.ServerIdentifier())
	}
	if m.SubnetMask().String() != "ffffff00" {
		t.Fatalf("subnet mask = %s", m.SubnetMask())
	}
	if m.IPAddressLeaseTime(0) != time.Hour || m.IPAddressRenewalTime(0) != 30*time.Minute || m.IPAddressRebindingTime(0) != 52*time.Minute+30*time.Second {
		t.Fatalf("lease times %v %v %v", m.IPAddressLeaseTime(0), m.IPAddressRenewalTime(0), m.IPAddressRebindingTime(0))
	}
	if r.Dst.String() != "255.255.255.255:68" || r.L2Unicast {
		t.Fatalf("broadcast flag not honoured: %+v", r.Dst)
	}

	// Without broadcast flag the reply is L2 unicast to yiaddr.
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover), "255.255.255.255")
	if r == nil || !r.L2Unicast || r.Dst.Addr().String() != "10.0.0.100" {
		t.Fatalf("expected L2 unicast to yiaddr, got %+v", r)
	}
}

func TestRequestSelectingAndOtherServer(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	_, ack := e.dora()
	if !ack.Msg.YourIPAddr.Equal(mustIP("10.0.0.100")) {
		t.Fatalf("ack yiaddr %s", ack.Msg.YourIPAddr)
	}
	if l, ok := e.leases.Get(ip4.MustFromAddr(netip.MustParseAddr("10.0.0.100"))); !ok || l.State != lease.StateActive {
		t.Fatalf("lease not active: %+v", l)
	}

	// A client selecting another server: no reply, offer dropped.
	other := net.HardwareAddr{0x02, 0, 0, 0, 0, 0x02}
	off := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(other), dhcpv4.WithBroadcast(true)), "255.255.255.255")
	if off == nil {
		t.Fatal("no offer")
	}
	r := e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithHwAddr(other),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(mustIP("10.0.0.254"))),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(off.Msg.YourIPAddr))), "255.255.255.255")
	if r != nil {
		t.Fatal("answered a request for another server")
	}
	// The address is free again for a third client right away.
	third := net.HardwareAddr{0x02, 0, 0, 0, 0, 0x03}
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(third),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(off.Msg.YourIPAddr))), "255.255.255.255")
	if r == nil || !r.Msg.YourIPAddr.Equal(off.Msg.YourIPAddr) {
		t.Fatalf("cancelled offer not released: %v", r)
	}
}

func TestInitRebootAndRenew(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	_, ack := e.dora()
	ip := ack.Msg.YourIPAddr

	// INIT-REBOOT with the right address: ACK.
	r := e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ip))), "255.255.255.255")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("init-reboot not acked: %v", r)
	}
	// INIT-REBOOT from another network: NAK, broadcast.
	r = e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(mustIP("192.168.5.5")))), "255.255.255.255")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeNak || r.Dst.String() != "255.255.255.255:68" {
		t.Fatalf("expected broadcast NAK, got %v", r)
	}
	if !r.Msg.YourIPAddr.Equal(net.IPv4zero) || r.Msg.Options.Has(dhcpv4.OptionIPAddressLeaseTime) {
		t.Fatal("NAK carries lease data")
	}
	// RENEWING (unicast, ciaddr set): ACK sent to ciaddr.
	r = e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithClientIP(ip)), "10.0.0.1")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeAck || r.Dst.String() != ip.String()+":68" {
		t.Fatalf("renew: %v", r)
	}
	if !r.Msg.ClientIPAddr.Equal(ip) {
		t.Fatal("ciaddr not echoed")
	}
	// RENEWING by a different client for that address: NAK.
	r = e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 9}), dhcpv4.WithClientIP(ip)), "10.0.0.1")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("foreign renew: %v", r)
	}
}

func TestInitRebootOnLinkWithoutScopeIsSilent(t *testing.T) {
	e := newHEnv(t)
	r := e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(mustIP("10.0.0.50")))), "255.255.255.255")
	if r != nil {
		t.Fatal("answered on a link without scope")
	}
}

func TestReleaseAndDecline(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	_, ack := e.dora()
	ip := ack.Msg.YourIPAddr
	sid := dhcpv4.WithOption(dhcpv4.OptServerIdentifier(mustIP("10.0.0.1")))

	if r := e.handle(msg(t, dhcpv4.MessageTypeRelease, dhcpv4.WithClientIP(ip), sid), "10.0.0.1"); r != nil {
		t.Fatal("release must not be answered")
	}
	if l, _ := e.leases.Get(u32(ip)); l.State != lease.StateReleased {
		t.Fatalf("state after release: %s", l.State)
	}

	_, ack = e.dora()
	ip = ack.Msg.YourIPAddr
	if r := e.handle(msg(t, dhcpv4.MessageTypeDecline, sid, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ip))), "255.255.255.255"); r != nil {
		t.Fatal("decline must not be answered")
	}
	if l, _ := e.leases.Get(u32(ip)); l.State != lease.StateDeclined {
		t.Fatalf("state after decline: %s", l.State)
	}
	// Next DORA gives a different address.
	_, ack = e.dora()
	if ack.Msg.YourIPAddr.Equal(ip) {
		t.Fatal("declined address handed out again")
	}
}

func TestInform(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	r := e.handle(msg(t, dhcpv4.MessageTypeInform, dhcpv4.WithClientIP(mustIP("10.0.0.20"))), "10.0.0.1")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("inform: %v", r)
	}
	if r.Msg.Options.Has(dhcpv4.OptionIPAddressLeaseTime) || !r.Msg.YourIPAddr.Equal(net.IPv4zero) {
		t.Fatal("inform reply must not carry a lease")
	}
	if r.Dst.String() != "10.0.0.20:68" {
		t.Fatalf("inform reply dst = %s", r.Dst)
	}
}

func TestRelayAndOption82(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	e.addScope("remote", "172.16.5.0/24", "172.16.5.10", "172.16.5.20", time.Hour)
	rai := dhcpv4.OptRelayAgentInfo(
		dhcpv4.OptGeneric(dhcpv4.AgentCircuitIDSubOption, []byte("port-7")),
	)
	m := msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithGatewayIP(mustIP("172.16.5.1")), dhcpv4.WithOption(rai))
	m.HopCount = 1
	r := e.handle(m, "10.0.0.1")
	if r == nil || !r.Msg.YourIPAddr.Equal(mustIP("172.16.5.10")) {
		t.Fatalf("relay offer: %v", r)
	}
	if r.Dst.String() != "172.16.5.1:67" {
		t.Fatalf("reply not sent to relay: %s", r.Dst)
	}
	if !r.Msg.GatewayIPAddr.Equal(mustIP("172.16.5.1")) {
		t.Fatal("giaddr not echoed")
	}
	got := r.Msg.RelayAgentInfo()
	if got == nil || string(got.Get(dhcpv4.AgentCircuitIDSubOption)) != "port-7" {
		t.Fatal("option 82 not echoed")
	}
	if !r.Msg.ServerIdentifier().Equal(mustIP("10.0.0.1")) {
		t.Fatalf("server id for relayed packet = %s", r.Msg.ServerIdentifier())
	}

	// Relayed request for an address of another network: NAK via relay
	// with broadcast bit.
	req := msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithGatewayIP(mustIP("172.16.5.1")),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(mustIP("10.0.0.150"))))
	r = e.handle(req, "10.0.0.1")
	if r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeNak || r.Dst.String() != "172.16.5.1:67" || !r.Msg.IsBroadcast() {
		t.Fatalf("relayed NAK: %v", r)
	}

	// Link selection sub-option overrides giaddr.
	ls := dhcpv4.OptRelayAgentInfo(dhcpv4.OptGeneric(dhcpv4.LinkSelectionSubOption, mustIP("10.0.0.0")))
	m = msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 5}),
		dhcpv4.WithGatewayIP(mustIP("172.16.5.1")), dhcpv4.WithOption(ls))
	r = e.handle(m, "10.0.0.1")
	if r == nil || !r.Msg.YourIPAddr.Equal(mustIP("10.0.0.100")) {
		t.Fatalf("link selection ignored: %v", r)
	}

	// Server identifier override sub-option.
	so := dhcpv4.OptRelayAgentInfo(dhcpv4.OptGeneric(dhcpv4.ServerIdentifierOverrideSubOption, mustIP("172.16.5.1")))
	m = msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 6}),
		dhcpv4.WithGatewayIP(mustIP("172.16.5.1")), dhcpv4.WithOption(so))
	r = e.handle(m, "10.0.0.1")
	if r == nil || !r.Msg.ServerIdentifier().Equal(mustIP("172.16.5.1")) {
		t.Fatalf("server id override ignored: %v", r)
	}
}

func TestUnlimitedLease(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", 0)
	_, ack := e.dora()
	lt := ack.Msg.Options.Get(dhcpv4.OptionIPAddressLeaseTime)
	if len(lt) != 4 || lt[0] != 0xff || lt[3] != 0xff {
		t.Fatalf("lease time = %x", lt)
	}
	if ack.Msg.Options.Has(dhcpv4.OptionRenewTimeValue) || ack.Msg.Options.Has(dhcpv4.OptionRebindingTimeValue) {
		t.Fatal("T1/T2 sent for an unlimited lease")
	}
}

func TestIgnoresGarbage(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	m := msg(t, dhcpv4.MessageTypeDiscover)
	m.OpCode = dhcpv4.OpcodeBootReply
	if e.handle(m, "255.255.255.255") != nil {
		t.Fatal("answered a BOOTREPLY")
	}
	m = msg(t, dhcpv4.MessageTypeDiscover)
	m.Options.Del(dhcpv4.OptionDHCPMessageType)
	if e.handle(m, "255.255.255.255") != nil {
		t.Fatal("answered BOOTP")
	}
}

func TestHostnameSanitized(t *testing.T) {
	if got := sanitizeHostname("my host;rm -rf/\x00x"); got != "myhostrm-rfx" {
		t.Fatalf("got %q", got)
	}
}

package dhcp4

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
)

func TestSuperscopeExtendsTheLink(t *testing.T) {
	e := newHEnv(t)
	ctx := context.Background()
	a := e.addScope("a", "10.0.0.0/24", "10.0.0.100", "10.0.0.101", time.Hour)
	b := e.addScope("b", "10.0.1.0/24", "10.0.1.100", "10.0.1.200", time.Hour)
	if _, err := e.scopes.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: b.ID}, Code: 3, Value: "10.0.1.1"}); err != nil {
		t.Fatal(err)
	}
	discover := func(mac byte) *Response {
		m := msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true), dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 1, mac}))
		return e.handle(m, "255.255.255.255")
	}
	for i := range 2 {
		r := discover(byte(i))
		if r == nil {
			t.Fatal("no offer from the first scope")
		}
		ack := e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithBroadcast(true), dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 1, byte(i)}),
			dhcpv4.WithOption(dhcpv4.OptServerIdentifier(r.Msg.ServerIdentifier())), dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(r.Msg.YourIPAddr))), "255.255.255.255")
		if ack == nil || ack.Msg.MessageType() != dhcpv4.MessageTypeAck {
			t.Fatal("no ack")
		}
	}
	// Scope a is exhausted and b is not on the link: no address.
	if r := discover(9); r != nil {
		t.Fatalf("offer without superscope: %s", r.Msg.YourIPAddr)
	}
	ss, err := e.scopes.CreateSuperscope(ctx, scope.Superscope{Name: "multinet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*scope.Pool{a, b} {
		sc := p.Scope
		sc.SuperscopeID = ss.ID
		if _, err := e.scopes.UpdateScope(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	r := discover(9)
	if r == nil || !r.Msg.YourIPAddr.Equal(mustIP("10.0.1.100")) {
		t.Fatalf("superscope member not used: %v", r)
	}
	if r.Msg.SubnetMask().String() != "ffffff00" || !r.Msg.Router()[0].Equal(mustIP("10.0.1.1")) {
		t.Fatal("options of the member scope missing")
	}
	if !r.Msg.ServerIdentifier().Equal(mustIP("10.0.0.1")) {
		t.Fatalf("server id %s", r.Msg.ServerIdentifier())
	}
}

func bootRequest(t *testing.T, mac net.HardwareAddr) *dhcpv4.DHCPv4 {
	t.Helper()
	m, err := dhcpv4.New(dhcpv4.WithHwAddr(mac), dhcpv4.WithBroadcast(true))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBOOTP(t *testing.T) {
	e := newHEnv(t)
	ctx := context.Background()
	p := e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	for code, v := range map[uint8]string{3: "10.0.0.1", 6: "10.0.0.53, 10.0.0.54", 15: "example.org", 66: "10.0.0.5", 67: "boot/legacy.img"} {
		if _, err := e.scopes.SetOption(ctx, scope.OptionValue{Owner: scope.Owner{Kind: scope.OwnerScope, ID: p.ID}, Code: code, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	mac := net.HardwareAddr{2, 0, 0, 0, 2, 1}
	// No reservation, BOOTP not enabled: no answer.
	if r := e.handle(bootRequest(t, mac), "255.255.255.255"); r != nil {
		t.Fatal("BOOTP answered although not enabled")
	}
	// Reservation: answered with the reserved address.
	if _, err := e.scopes.CreateReservation(ctx, scope.Reservation{ScopeID: p.ID, IP: netip.MustParseAddr("10.0.0.50"), MAC: mac.String()}); err != nil {
		t.Fatal(err)
	}
	r := e.handle(bootRequest(t, mac), "255.255.255.255")
	if r == nil {
		t.Fatal("reserved BOOTP client not answered")
	}
	m := r.Msg
	if m.OpCode != dhcpv4.OpcodeBootReply || m.MessageType() != dhcpv4.MessageTypeNone || !m.YourIPAddr.Equal(mustIP("10.0.0.50")) {
		t.Fatalf("reply %s", m.Summary())
	}
	if m.Options.Has(dhcpv4.OptionIPAddressLeaseTime) || m.Options.Has(dhcpv4.OptionServerIdentifier) {
		t.Fatal("DHCP-only options in a BOOTP reply")
	}
	if m.SubnetMask().String() != "ffffff00" || !m.Router()[0].Equal(mustIP("10.0.0.1")) || len(m.DNS()) != 2 {
		t.Fatalf("vendor options %v", m.Options)
	}
	if size := 4 + len(m.Options.ToBytes()); size > bootpVendorArea {
		t.Fatalf("vendor area %d bytes", size)
	}
	if m.BootFileName != "boot/legacy.img" || !m.ServerIPAddr.Equal(mustIP("10.0.0.5")) {
		t.Fatalf("boot fields %q %s", m.BootFileName, m.ServerIPAddr)
	}
	if l, ok := e.leases.Get(ip4.MustFromAddr(netip.MustParseAddr("10.0.0.50"))); !ok || l.Expires.Sub(l.Start) != DefaultBOOTPLeaseTime {
		t.Fatalf("BOOTP lease %+v", l)
	}
	// Dynamic BOOTP once enabled on the scope.
	sc := p.Scope
	sc.BOOTP = true
	if _, err := e.scopes.UpdateScope(ctx, sc); err != nil {
		t.Fatal(err)
	}
	r = e.handle(bootRequest(t, net.HardwareAddr{2, 0, 0, 0, 2, 2}), "255.255.255.255")
	if r == nil || !r.Msg.YourIPAddr.Equal(mustIP("10.0.0.100")) {
		t.Fatalf("dynamic BOOTP: %v", r)
	}
}

package dhcp4

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/scope"
)

func (e *henv) setOpt(o scope.Owner, code uint8, v string) {
	e.t.Helper()
	if _, err := e.scopes.SetOption(context.Background(), scope.OptionValue{Owner: o, Code: code, Value: v}); err != nil {
		e.t.Fatal(err)
	}
}

func prl(codes ...uint8) dhcpv4.Modifier {
	var l dhcpv4.OptionCodeList
	for _, c := range codes {
		l.Add(dhcpv4.GenericOptionCode(c))
	}
	return dhcpv4.WithOption(dhcpv4.OptParameterRequestList(l...))
}

func TestOptionsHonourParameterRequestList(t *testing.T) {
	e := newHEnv(t)
	p := e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	e.setOpt(scope.ServerOwner, 6, "1.1.1.1")
	e.setOpt(scope.Owner{Kind: scope.OwnerScope, ID: p.ID}, 3, "10.0.0.1")
	e.setOpt(scope.Owner{Kind: scope.OwnerScope, ID: p.ID}, 15, "lan.example")

	r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true), prl(1, 3, 6)), "255.255.255.255")
	if r == nil {
		t.Fatal("no offer")
	}
	if got := r.Msg.Router(); len(got) != 1 || !got[0].Equal(mustIP("10.0.0.1")) {
		t.Errorf("router = %v", got)
	}
	if got := r.Msg.DNS(); len(got) != 1 || !got[0].Equal(mustIP("1.1.1.1")) {
		t.Errorf("dns = %v", got)
	}
	if r.Msg.Options.Has(dhcpv4.OptionDomainName) {
		t.Error("unrequested option 15 sent")
	}
	// Without a request list every configured option is sent.
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 7}), dhcpv4.WithBroadcast(true)), "255.255.255.255")
	if r.Msg.DomainName() != "lan.example" || len(r.Msg.Router()) != 1 {
		t.Errorf("options without PRL: %s", r.Msg.Summary())
	}
}

func TestPXEPolicyBootOptions(t *testing.T) {
	e := newHEnv(t)
	p := e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	ctx := context.Background()
	pol, err := e.scopes.CreatePolicy(ctx, scope.Policy{ScopeID: p.ID, Name: "pxe", Enabled: true,
		Conditions: []scope.Condition{{Field: scope.FieldVendorClass, Value: "PXEClient*"}}})
	if err != nil {
		t.Fatal(err)
	}
	po := scope.Owner{Kind: scope.OwnerPolicy, ID: pol.ID}
	e.setOpt(po, 66, "10.0.0.5")
	e.setOpt(po, 67, "pxelinux.0")
	e.setOpt(po, 60, "PXEClient")

	pxe := []dhcpv4.Modifier{
		dhcpv4.WithBroadcast(true),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient:Arch:00000:UNDI:002001")),
		prl(1, 3, 43, 60, 66, 67),
	}
	r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, pxe...), "255.255.255.255")
	if r == nil {
		t.Fatal("no offer for PXE client")
	}
	m := r.Msg
	if m.TFTPServerName() != "10.0.0.5" || m.BootFileNameOption() != "pxelinux.0" || m.ClassIdentifier() != "PXEClient" {
		t.Fatalf("boot options missing: %s", m.Summary())
	}
	if m.BootFileName != "pxelinux.0" || !m.ServerIPAddr.Equal(mustIP("10.0.0.5")) || m.ServerHostName != "10.0.0.5" {
		t.Fatalf("BOOTP header fields: file=%q siaddr=%s sname=%q", m.BootFileName, m.ServerIPAddr, m.ServerHostName)
	}
	// ACK carries them too.
	ack := e.handle(msg(t, dhcpv4.MessageTypeRequest, append(pxe,
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(m.ServerIdentifier())),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(m.YourIPAddr)))...), "255.255.255.255")
	if ack == nil || ack.Msg.BootFileNameOption() != "pxelinux.0" || ack.Msg.BootFileName != "pxelinux.0" {
		t.Fatalf("ack: %v", ack)
	}
	// A normal client does not get boot options.
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 9}), dhcpv4.WithBroadcast(true)), "255.255.255.255")
	if r.Msg.Options.Has(dhcpv4.OptionBootfileName) || r.Msg.BootFileName != "" {
		t.Fatal("boot options sent to non-PXE client")
	}
}

func TestDeniedMACGetsNoAnswer(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	ctx := context.Background()
	if _, err := e.scopes.AddFilter(ctx, scope.MACFilter{List: scope.FilterDeny, Pattern: "02:00:00:00:00:*"}); err != nil {
		t.Fatal(err)
	}
	if err := e.scopes.SetFilterSettings(ctx, scope.FilterSettings{DenyEnabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, mt := range []dhcpv4.MessageType{dhcpv4.MessageTypeDiscover, dhcpv4.MessageTypeRequest, dhcpv4.MessageTypeInform} {
		m := msg(t, mt, dhcpv4.WithClientIP(mustIP("10.0.0.20")), dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(mustIP("10.0.0.150"))))
		if r := e.handle(m, "255.255.255.255"); r != nil {
			t.Fatalf("%s from denied MAC answered", mt)
		}
	}
	// Other MACs are served.
	if r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{0xaa, 0, 0, 0, 0, 1})), "255.255.255.255"); r == nil {
		t.Fatal("allowed MAC not served")
	}
}

func TestPolicyRangeAndLeaseTime(t *testing.T) {
	e := newHEnv(t)
	p := e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	lt := 10 * time.Minute
	if _, err := e.scopes.CreatePolicy(context.Background(), scope.Policy{ScopeID: p.ID, Name: "phones", Enabled: true, LeaseTime: &lt,
		Conditions: []scope.Condition{{Field: scope.FieldVendorClass, Value: "Polycom*"}},
		Ranges:     []scope.AddrRange{{Start: netip.MustParseAddr("10.0.0.180"), End: netip.MustParseAddr("10.0.0.189")}}}); err != nil {
		t.Fatal(err)
	}
	r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("Polycom-VVX-400"))), "255.255.255.255")
	if r == nil || !r.Msg.YourIPAddr.Equal(mustIP("10.0.0.180")) || r.Msg.IPAddressLeaseTime(0) != lt {
		t.Fatalf("phone offer: %v", r)
	}
	// Non-matching clients never get addresses from the policy range.
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 3}),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(mustIP("10.0.0.185")))), "255.255.255.255")
	if r == nil || r.Msg.YourIPAddr.Equal(mustIP("10.0.0.185")) || r.Msg.IPAddressLeaseTime(0) != time.Hour {
		t.Fatalf("non-phone offer: %v", r)
	}
}

func TestMaxMessageSizeDropsOptions(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	e.setOpt(scope.ServerOwner, 15, "lan.example")
	e.setOpt(scope.ServerOwner, 252, "http://"+strings.Repeat("w", 250)+"/wpad.dat")
	e.setOpt(scope.ServerOwner, 114, "https://"+strings.Repeat("c", 240)+"/api")
	r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true), prl(15, 252, 114)), "255.255.255.255")
	if r == nil {
		t.Fatal("no offer")
	}
	if n := len(r.Msg.ToBytes()); n > minMaxMessageSize-ipUDPHeaders {
		t.Fatalf("reply is %d bytes", n)
	}
	if r.Msg.DomainName() != "lan.example" || r.Msg.Options.Has(dhcpv4.GenericOptionCode(114)) {
		t.Fatalf("expected the last requested option to be dropped: %s", r.Msg.Summary())
	}
	// With a larger max message size everything fits.
	r = e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithHwAddr(net.HardwareAddr{2, 0, 0, 0, 0, 4}), dhcpv4.WithBroadcast(true),
		prl(15, 252, 114), dhcpv4.WithOption(dhcpv4.OptMaxMessageSize(1500))), "255.255.255.255")
	if !r.Msg.Options.Has(dhcpv4.GenericOptionCode(114)) || !r.Msg.Options.Has(dhcpv4.GenericOptionCode(252)) {
		t.Fatal("options dropped despite large max message size")
	}
}

package dhcp4

import (
	"context"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/dnsupdate"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

type boundCall struct {
	l lease.Lease
	d dnsupdate.Decision
}

type fakeDNSUpdater struct{ calls []boundCall }

func (f *fakeDNSUpdater) Bound(l lease.Lease, _ scope.DNSSettings, d dnsupdate.Decision, _ time.Duration) {
	f.calls = append(f.calls, boundCall{l, d})
}

func TestClientFQDNAndDNSRegistration(t *testing.T) {
	e := newHEnv(t)
	p := e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	sc := p.Scope
	sc.DNS = scope.DNSSettings{Mode: scope.DNSClient, Domain: "lan.example", Cleanup: true, Protect: true}
	if _, err := e.scopes.UpdateScope(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	up := &fakeDNSUpdater{}
	e.h.SetDNS(up)

	// Option 81 in wire format, S=1, partial name "Laptop".
	fqdn := dhcpv4.OptGeneric(dhcpv4.OptionFQDN, []byte{0x05, 0, 0, 6, 'L', 'a', 'p', 't', 'o', 'p'})
	offer := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true), dhcpv4.WithOption(fqdn)), "255.255.255.255")
	want := []byte{0x05, 255, 255, 6, 'l', 'a', 'p', 't', 'o', 'p', 3, 'l', 'a', 'n', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0}
	if got := offer.Msg.Options.Get(dhcpv4.OptionFQDN); string(got) != string(want) {
		t.Fatalf("offer option 81 = %x, want %x", got, want)
	}
	ack := e.handle(msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithBroadcast(true), dhcpv4.WithOption(fqdn),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(offer.Msg.ServerIdentifier())),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offer.Msg.YourIPAddr))), "255.255.255.255")
	if got := ack.Msg.Options.Get(dhcpv4.OptionFQDN); string(got) != string(want) {
		t.Fatalf("ack option 81 = %x", got)
	}
	if len(up.calls) != 1 {
		t.Fatalf("Bound called %d times", len(up.calls))
	}
	c := up.calls[0]
	if c.d.FQDN != "laptop.lan.example" || !c.d.Forward || !c.d.Reverse || c.l.Hostname != "laptop" || !ip4.ToIP(c.l.IP).Equal(ack.Msg.YourIPAddr) {
		t.Fatalf("Bound(%+v, %+v)", c.l, c.d)
	}

	// Without option 81 there is no option in the reply; "client" mode
	// registers nothing.
	_, ack = e.dora()
	if ack.Msg.Options.Has(dhcpv4.OptionFQDN) {
		t.Fatal("unexpected option 81")
	}
	if last := up.calls[len(up.calls)-1]; last.d.Updates() {
		t.Fatalf("client without option 81 registered: %+v", last.d)
	}
}

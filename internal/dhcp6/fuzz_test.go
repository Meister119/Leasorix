package dhcp6

import (
	"net/netip"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv6"
)

// FuzzHandle6 feeds arbitrary packets through parser and handler: no
// panics, and every reply must encode to a parseable message.
func FuzzHandle6(f *testing.F) {
	seed := func(mt dhcpv6.MessageType, opts ...dhcpv6.Option) {
		m := &dhcpv6.Message{MessageType: mt, TransactionID: dhcpv6.TransactionID{1, 2, 3}}
		m.AddOption(dhcpv6.OptClientID(duid(1)))
		for _, o := range opts {
			m.AddOption(o)
		}
		f.Add(m.ToBytes())
	}
	seed(dhcpv6.MessageTypeSolicit, ianaOpt(1), iapdOpt(2))
	seed(dhcpv6.MessageTypeRequest, dhcpv6.OptServerID(serverDUID), ianaOpt(1, "2001:db8:1::100"))
	seed(dhcpv6.MessageTypeRebind, ianaOpt(1, "2001:db8:1::100"), iapdOpt(2, "2001:db8:100::/56"))
	inner := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeSolicit}
	inner.AddOption(dhcpv6.OptClientID(duid(2)))
	if r, err := dhcpv6.EncapsulateRelay(inner, dhcpv6.MessageTypeRelayForward, netip.MustParseAddr("2001:db8:1::fe").AsSlice(), netip.MustParseAddr("fe80::1").AsSlice()); err == nil {
		f.Add(r.ToBytes())
	}
	base := newHEnv(f, nil)
	f.Fuzz(func(t *testing.T, b []byte) {
		e := *base
		e.t = t
		m, err := dhcpv6.FromBytes(b)
		if err != nil {
			return
		}
		r := e.handle(m, multicast)
		if r == nil {
			return
		}
		if _, err := dhcpv6.FromBytes(r.Msg.ToBytes()); err != nil {
			t.Fatalf("reply does not parse: %v", err)
		}
	})
}

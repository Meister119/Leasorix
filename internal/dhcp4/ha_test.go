package dhcp4

import (
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// fakePartner answers only directed messages unless all is set.
type fakePartner struct {
	all      bool
	directed []bool
}

func (f *fakePartner) Accept(_ string, directed bool) (bool, string) {
	f.directed = append(f.directed, directed)
	if f.all || directed {
		return true, ""
	}
	return false, "ha_partner"
}

func TestPartnerDecidesWhoAnswers(t *testing.T) {
	e := newHEnv(t)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)
	fp := &fakePartner{all: true}
	e.h.SetPartner(fp)
	_, ack := e.dora()
	if fp.directed[0] || !fp.directed[1] {
		t.Fatalf("DISCOVER must be undirected, REQUEST with our server id directed: %v", fp.directed)
	}

	fp.all = false
	if r := e.handle(msg(t, dhcpv4.MessageTypeDiscover, dhcpv4.WithBroadcast(true)), "255.255.255.255"); r != nil {
		t.Fatal("broadcast DISCOVER answered although the partner is responsible")
	}
	// Renewal unicast to our address: directed, answered.
	renew := msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithClientIP(ack.Msg.YourIPAddr))
	if r := e.handle(renew, "10.0.0.1"); r == nil || r.Msg.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatal("unicast renewal not answered")
	}
	// Rebinding (broadcast) and INIT-REBOOT are not directed.
	rebind := msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithClientIP(ack.Msg.YourIPAddr))
	if r := e.handle(rebind, "255.255.255.255"); r != nil {
		t.Fatal("broadcast rebinding answered although the partner is responsible")
	}
	reboot := msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ack.Msg.YourIPAddr)))
	if r := e.handle(reboot, "255.255.255.255"); r != nil {
		t.Fatal("INIT-REBOOT answered although the partner is responsible")
	}
	// A REQUEST carrying another server's identifier is not directed.
	other := msg(t, dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(mustIP("10.0.0.2"))),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ack.Msg.YourIPAddr)))
	e.handle(other, "255.255.255.255")
	if fp.directed[len(fp.directed)-1] {
		t.Fatal("foreign server id treated as directed")
	}
}

package dhcp4

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// FuzzHandle feeds arbitrary bytes through the parser and the state machine.
// The handler must never panic and must only answer with replies.
//
//	go test -run '^$' -fuzz FuzzHandle -fuzztime 60s ./internal/dhcp4
func FuzzHandle(f *testing.F) {
	seeds := []dhcpv4.MessageType{
		dhcpv4.MessageTypeDiscover, dhcpv4.MessageTypeRequest, dhcpv4.MessageTypeRelease,
		dhcpv4.MessageTypeDecline, dhcpv4.MessageTypeInform,
	}
	for _, mt := range seeds {
		m, _ := dhcpv4.New(dhcpv4.WithMessageType(mt), dhcpv4.WithHwAddr(testMAC),
			dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.IPv4(10, 0, 0, 120))),
			dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(10, 0, 0, 1))),
			dhcpv4.WithOption(dhcpv4.OptRelayAgentInfo(dhcpv4.OptGeneric(dhcpv4.LinkSelectionSubOption, []byte{10, 0, 0, 0}))))
		f.Add(m.ToBytes())
	}
	f.Add([]byte{})
	f.Add(make([]byte, 240))

	e := newHEnv(f)
	e.addScope("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200", time.Hour)

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := dhcpv4.FromBytes(data)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r := e.h.Handle(ctx, &Request{
			Msg: m, IfName: "eth0", IfAddrs: e.ifAddr,
			Src: netip.MustParseAddrPort("0.0.0.0:68"), Dst: netip.MustParseAddr("255.255.255.255"),
		})
		if r != nil {
			if r.Msg.OpCode != dhcpv4.OpcodeBootReply {
				t.Fatalf("reply with opcode %v", r.Msg.OpCode)
			}
			_ = r.Msg.ToBytes()
		}
	})
}

package ip4

import (
	"net"
	"net/netip"
	"testing"
)

func TestConversions(t *testing.T) {
	v, err := Parse("10.0.0.1")
	if err != nil || v != 0x0A000001 {
		t.Fatalf("Parse = %x, %v", v, err)
	}
	if String(v) != "10.0.0.1" {
		t.Fatalf("String = %s", String(v))
	}
	if u, ok := FromIP(net.ParseIP("192.168.1.255")); !ok || u != 0xC0A801FF {
		t.Fatalf("FromIP = %x %v", u, ok)
	}
	if !ToIP(0xC0A80101).Equal(net.IPv4(192, 168, 1, 1)) {
		t.Fatal("ToIP")
	}
	if u, ok := FromAddr(netip.MustParseAddr("::ffff:1.2.3.4")); !ok || u != 0x01020304 {
		t.Fatal("mapped address not unmapped")
	}
	if _, err := Parse("::1"); err == nil {
		t.Fatal("IPv6 accepted")
	}
	if _, ok := FromIP(net.ParseIP("fe80::1")); ok {
		t.Fatal("IPv6 accepted by FromIP")
	}
}

func TestMaskMath(t *testing.T) {
	ip := uint32(0xC0A8010A) // 192.168.1.10
	if Mask(24) != 0xFFFFFF00 || Mask(0) != 0 || Mask(32) != 0xFFFFFFFF {
		t.Fatal("Mask")
	}
	if Network(ip, 24) != 0xC0A80100 || Broadcast(ip, 24) != 0xC0A801FF {
		t.Fatal("Network/Broadcast")
	}
	if !Contains(0xC0A80100, 24, ip) || Contains(0xC0A80200, 24, ip) {
		t.Fatal("Contains")
	}
}

func TestRange(t *testing.T) {
	a := Range{10, 20}
	if !a.Contains(10) || !a.Contains(20) || a.Contains(21) {
		t.Fatal("Contains")
	}
	if !a.Overlaps(Range{20, 30}) || a.Overlaps(Range{21, 30}) || !a.Overlaps(Range{0, 10}) {
		t.Fatal("Overlaps")
	}
	if a.Size() != 11 || (Range{0, 0xFFFFFFFF}).Size() != 1<<32 {
		t.Fatal("Size")
	}
}

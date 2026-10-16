//go:build linux

package dhcp4

import (
	"encoding/binary"
	"testing"
)

func TestBuildIPv4UDP(t *testing.T) {
	payload := []byte("hello dhcp")
	src, dst := [4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 100}
	pkt := buildIPv4UDP(src, dst, 67, 68, payload)
	if len(pkt) != 20+8+len(payload) {
		t.Fatalf("len = %d", len(pkt))
	}
	// A correct IPv4 header checksums to zero.
	if checksum(pkt[:20], 0) != 0 {
		t.Fatal("bad IPv4 header checksum")
	}
	if binary.BigEndian.Uint16(pkt[2:]) != uint16(len(pkt)) || pkt[9] != 17 {
		t.Fatal("bad length or protocol")
	}
	// UDP checksum incl. pseudo header verifies to zero as well.
	u := pkt[20:]
	pseudo := uint32(0x0a00) + 0x0001 + 0x0a00 + 0x0064 + 17 + uint32(len(u))
	if checksum(u, pseudo) != 0 {
		t.Fatal("bad UDP checksum")
	}
	if binary.BigEndian.Uint16(u[0:]) != 67 || binary.BigEndian.Uint16(u[2:]) != 68 || string(u[8:]) != "hello dhcp" {
		t.Fatal("bad UDP header/payload")
	}
}

func TestChecksumOddLength(t *testing.T) {
	// RFC 1071 example data with an odd trailing byte.
	if got := checksum([]byte{0x00, 0x01, 0xf2}, 0); got != ^uint16(0x0001+0xf200) {
		t.Fatalf("got %x", got)
	}
}

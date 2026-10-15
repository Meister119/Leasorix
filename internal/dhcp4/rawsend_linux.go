//go:build linux

package dhcp4

import (
	"encoding/binary"
	"math/rand/v2"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// rawSender delivers IPv4/UDP packets directly to a hardware address via an
// AF_PACKET datagram socket. It is used for OFFER/ACK to clients that have
// no address yet and did not request a broadcast reply (RFC 2131 4.1).
// The socket is opened with protocol 0 so it never receives traffic.
type rawSender struct {
	fd int
}

func newRawSender() (*rawSender, error) {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &rawSender{fd: fd}, nil
}

func (r *rawSender) close() { _ = unix.Close(r.fd) }

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (r *rawSender) send(ifindex int, mac net.HardwareAddr, src, dst netip.Addr, sport, dport uint16, payload []byte) error {
	pkt := buildIPv4UDP(src.As4(), dst.As4(), sport, dport, payload)
	sa := &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_IP), Ifindex: ifindex, Halen: uint8(len(mac))}
	copy(sa.Addr[:], mac)
	return unix.Sendto(r.fd, pkt, 0, sa)
}

// buildIPv4UDP builds an IPv4 header + UDP header + payload with valid
// checksums.
func buildIPv4UDP(src, dst [4]byte, sport, dport uint16, payload []byte) []byte {
	udpLen := 8 + len(payload)
	total := 20 + udpLen
	b := make([]byte, total)
	b[0] = 0x45 // version 4, IHL 5
	binary.BigEndian.PutUint16(b[2:], uint16(total))
	binary.BigEndian.PutUint16(b[4:], uint16(rand.Uint32())) //nolint:gosec // IP identification field, no security relevance
	b[8] = 64                                                // TTL
	b[9] = unix.IPPROTO_UDP
	copy(b[12:16], src[:])
	copy(b[16:20], dst[:])
	binary.BigEndian.PutUint16(b[10:], checksum(b[:20], 0))

	u := b[20:]
	binary.BigEndian.PutUint16(u[0:], sport)
	binary.BigEndian.PutUint16(u[2:], dport)
	binary.BigEndian.PutUint16(u[4:], uint16(udpLen))
	copy(u[8:], payload)
	var pseudo uint32
	pseudo += uint32(src[0])<<8 | uint32(src[1])
	pseudo += uint32(src[2])<<8 | uint32(src[3])
	pseudo += uint32(dst[0])<<8 | uint32(dst[1])
	pseudo += uint32(dst[2])<<8 | uint32(dst[3])
	pseudo += unix.IPPROTO_UDP
	pseudo += uint32(udpLen)
	cs := checksum(u, pseudo)
	if cs == 0 {
		cs = 0xffff
	}
	binary.BigEndian.PutUint16(u[6:], cs)
	return b
}

func checksum(b []byte, initial uint32) uint16 {
	sum := initial
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// Package ip4 contains small helpers for IPv4 address arithmetic. Addresses
// are represented as uint32 in host order (10.0.0.1 == 0x0A000001), which is
// also how they are stored in the database.
package ip4

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
)

// FromAddr converts an IPv4 (or IPv4-mapped IPv6) address to uint32.
// It returns false for non-IPv4 addresses.
func FromAddr(a netip.Addr) (uint32, bool) {
	a = a.Unmap()
	if !a.Is4() {
		return 0, false
	}
	b := a.As4()
	return binary.BigEndian.Uint32(b[:]), true
}

// MustFromAddr is FromAddr for addresses known to be IPv4. It returns 0 for
// anything else.
func MustFromAddr(a netip.Addr) uint32 {
	v, _ := FromAddr(a)
	return v
}

// ToAddr converts a uint32 to netip.Addr.
func ToAddr(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}

// FromIP converts a net.IP to uint32. Returns false if ip is not IPv4.
func FromIP(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip4), true
}

// ToIP converts a uint32 to a 4-byte net.IP.
func ToIP(v uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip
}

// Parse parses a dotted-quad IPv4 address.
func Parse(s string) (uint32, error) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return 0, err
	}
	v, ok := FromAddr(a)
	if !ok {
		return 0, fmt.Errorf("%q is not an IPv4 address", s)
	}
	return v, nil
}

// String formats v as dotted quad.
func String(v uint32) string { return ToAddr(v).String() }

// Mask returns the netmask for a prefix length (0..32).
func Mask(bits int) uint32 {
	if bits <= 0 {
		return 0
	}
	if bits >= 32 {
		return 0xFFFFFFFF
	}
	return ^uint32(0) << (32 - bits)
}

// Network returns the network address of ip for the prefix length.
func Network(ip uint32, bits int) uint32 { return ip & Mask(bits) }

// Broadcast returns the directed broadcast address of ip for the prefix length.
func Broadcast(ip uint32, bits int) uint32 { return ip | ^Mask(bits) }

// Contains reports whether ip lies in network/bits.
func Contains(network uint32, bits int, ip uint32) bool {
	return Network(ip, bits) == Network(network, bits)
}

// Range is an inclusive range of IPv4 addresses.
type Range struct {
	Start, End uint32
}

// Contains reports whether ip lies in the range.
func (r Range) Contains(ip uint32) bool { return ip >= r.Start && ip <= r.End }

// Overlaps reports whether two ranges share at least one address.
func (r Range) Overlaps(o Range) bool { return r.Start <= o.End && o.Start <= r.End }

// Size returns the number of addresses in the range.
func (r Range) Size() uint64 { return uint64(r.End) - uint64(r.Start) + 1 }

// String formats the range as "a-b".
func (r Range) String() string { return String(r.Start) + "-" + String(r.End) }

// Package ip6 provides 128-bit arithmetic on IPv6 addresses for range
// sizes, offsets and delegated prefixes.
package ip6

import (
	"encoding/binary"
	"math/bits"
	"net/netip"
)

// U128 is an IPv6 address as a 128-bit unsigned integer.
type U128 struct{ Hi, Lo uint64 }

// From converts an IPv6 address.
func From(a netip.Addr) U128 {
	b := a.As16()
	return U128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

// Addr converts back to an address.
func (u U128) Addr() netip.Addr {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], u.Hi)
	binary.BigEndian.PutUint64(b[8:], u.Lo)
	return netip.AddrFrom16(b)
}

// Add returns u + n (wrapping).
func (u U128) Add(n uint64) U128 {
	lo, carry := bits.Add64(u.Lo, n, 0)
	return U128{u.Hi + carry, lo}
}

// Sub returns u - v (wrapping).
func (u U128) Sub(v U128) U128 {
	lo, borrow := bits.Sub64(u.Lo, v.Lo, 0)
	return U128{u.Hi - v.Hi - borrow, lo}
}

// Shl returns u << n for n < 128.
func (u U128) Shl(n uint) U128 {
	switch {
	case n == 0:
		return u
	case n >= 64:
		return U128{u.Lo << (n - 64), 0}
	}
	return U128{u.Hi<<n | u.Lo>>(64-n), u.Lo << n}
}

// Cmp compares u and v.
func (u U128) Cmp(v U128) int {
	switch {
	case u.Hi < v.Hi, u.Hi == v.Hi && u.Lo < v.Lo:
		return -1
	case u == v:
		return 0
	}
	return 1
}

// RangeSize returns the number of addresses in [start, end] and whether
// it fits into a uint64 (false for more than 2^64-1 addresses).
func RangeSize(start, end netip.Addr) (uint64, bool) {
	d := From(end).Sub(From(start))
	if d.Hi != 0 || d.Lo == ^uint64(0) {
		return 0, false
	}
	return d.Lo + 1, true
}

// Offset returns start + n.
func Offset(start netip.Addr, n uint64) netip.Addr { return From(start).Add(n).Addr() }

// PrefixCount returns the number of /length prefixes in pool (length >
// pool bits, length-pool bits <= 63).
func PrefixCount(pool netip.Prefix, length int) uint64 {
	return uint64(1) << uint(length-pool.Bits())
}

// NthPrefix returns the i-th /length prefix of pool.
func NthPrefix(pool netip.Prefix, length int, i uint64) netip.Prefix {
	base := From(pool.Masked().Addr())
	off := U128{0, i}.Shl(uint(128 - length))
	sum := base
	lo, carry := bits.Add64(base.Lo, off.Lo, 0)
	sum.Lo, sum.Hi = lo, base.Hi+off.Hi+carry
	return netip.PrefixFrom(sum.Addr(), length)
}

// PrefixIndex returns the index of prefix p (of pool, /length) and whether
// it is one.
func PrefixIndex(pool netip.Prefix, length int, p netip.Prefix) (uint64, bool) {
	if p.Bits() != length || !pool.Contains(p.Addr()) || p.Masked() != p {
		return 0, false
	}
	d := From(p.Addr()).Sub(From(pool.Masked().Addr()))
	// d >> (128-length)
	n := uint(128 - length)
	var idx uint64
	if n >= 64 {
		idx = d.Hi >> (n - 64)
	} else {
		idx = d.Hi<<(64-n) | d.Lo>>n
	}
	return idx, true
}

// Bucket maps an address or prefix to 0..99 for the high availability
// split: the two halves are folded and spread with a Weyl sequence, so
// consecutive addresses as well as consecutive delegated prefixes are
// divided evenly.
func Bucket(a netip.Addr) int {
	u := From(a)
	x := (u.Hi ^ u.Lo) * 0x9E3779B97F4A7C15
	hi, _ := bits.Mul64(x, 100)
	return int(hi)
}

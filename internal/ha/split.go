package ha

import (
	"crypto/sha256"
	"encoding/binary"
	"net/netip"

	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/ip6"
)

// ClientBucket maps a client hardware address (canonical form) to 0..99.
// In load-balance mode the primary answers clients with a bucket below the
// configured percentage, the secondary all others. SHA-256 spreads even
// sequential MACs evenly.
func ClientBucket(mac string) int {
	sum := sha256.Sum256([]byte(mac))
	return int(binary.BigEndian.Uint64(sum[:8]) % 100)
}

// AddressBucket maps an address to 0..99 using the fractional part of
// ip * golden ratio (a Weyl sequence). Consecutive addresses are spread
// with very low discrepancy, so any contiguous range is split almost
// exactly by percentage, independent of the scope configuration.
func AddressBucket(ip uint32) int {
	frac := uint64(ip) * 0x9E3779B97F4A7C15 // 2^64 / golden ratio
	return int(((frac >> 32) * 100) >> 32)
}

// split decides which server answers a client and which server may
// allocate a free address.
type split struct {
	mode    string
	primary bool
	lb      int // load-balance: share of the primary
	reserve int // hot-standby: share of the standby
}

// responsible reports whether this server answers a broadcast from the
// client in NORMAL state.
func (s split) responsible(mac string) bool {
	if s.mode == config.HAHotStandby {
		return s.primary
	}
	return (ClientBucket(mac) < s.lb) == s.primary
}

// owns reports whether a free address belongs to this server's share.
func (s split) owns(ip uint32) bool { return s.ownsBucket(AddressBucket(ip)) }

func (s split) ownsBucket(b int) bool {
	if s.mode == config.HAHotStandby {
		return (b < s.reserve) != s.primary
	}
	return (b < s.lb) == s.primary
}

// owns6 reports whether an IPv6 address or delegated prefix belongs to
// this server's share.
func (s split) owns6(p netip.Prefix) bool { return s.ownsBucket(ip6.Bucket(p.Addr())) }

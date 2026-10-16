package ip6

import (
	"net/netip"
	"testing"
)

func TestArithmetic(t *testing.T) {
	a := netip.MustParseAddr("2001:db8::ffff:ffff:ffff:ffff")
	if got := Offset(a, 1); got != netip.MustParseAddr("2001:db8:0:1::") {
		t.Fatalf("carry: %s", got)
	}
	if From(a).Addr() != a {
		t.Fatal("round trip")
	}
	if n, ok := RangeSize(netip.MustParseAddr("2001:db8::100"), netip.MustParseAddr("2001:db8::1ff")); !ok || n != 256 {
		t.Fatalf("size %d %v", n, ok)
	}
	if _, ok := RangeSize(netip.MustParseAddr("2001:db8::"), netip.MustParseAddr("2001:db8::ffff:ffff:ffff:ffff")); ok {
		t.Fatal("2^64 addresses must not fit")
	}
	if From(netip.MustParseAddr("::1")).Cmp(From(netip.MustParseAddr("::2"))) != -1 {
		t.Fatal("cmp")
	}
}

func TestPrefixes(t *testing.T) {
	pool := netip.MustParsePrefix("2001:db8:100::/40")
	if n := PrefixCount(pool, 56); n != 65536 {
		t.Fatalf("count %d", n)
	}
	for _, tc := range []struct {
		i    uint64
		want string
	}{{0, "2001:db8:100::/56"}, {1, "2001:db8:100:100::/56"}, {255, "2001:db8:100:ff00::/56"}, {65535, "2001:db8:1ff:ff00::/56"}} {
		p := NthPrefix(pool, 56, tc.i)
		if p.String() != tc.want {
			t.Errorf("NthPrefix(%d) = %s, want %s", tc.i, p, tc.want)
		}
		if idx, ok := PrefixIndex(pool, 56, p); !ok || idx != tc.i {
			t.Errorf("PrefixIndex(%s) = %d %v", p, idx, ok)
		}
	}
	// /64 delegation: the index lives in the high half.
	if p := NthPrefix(netip.MustParsePrefix("2001:db8::/48"), 64, 7); p.String() != "2001:db8:0:7::/64" {
		t.Fatalf("/64: %s", p)
	}
	if _, ok := PrefixIndex(pool, 56, netip.MustParsePrefix("2001:db8:100::/60")); ok {
		t.Fatal("wrong length accepted")
	}
}

func TestBucketSpread(t *testing.T) {
	counts := make([]int, 2)
	base := netip.MustParseAddr("2001:db8::100")
	for i := range uint64(1000) {
		counts[min(Bucket(Offset(base, i))/50, 1)]++
	}
	if counts[0] < 450 || counts[0] > 550 {
		t.Fatalf("addresses split %v", counts)
	}
	counts = make([]int, 2)
	pool := netip.MustParsePrefix("2001:db8:100::/40")
	for i := range uint64(1000) {
		counts[min(Bucket(NthPrefix(pool, 56, i).Addr())/50, 1)]++
	}
	if counts[0] < 450 || counts[0] > 550 {
		t.Fatalf("prefixes split %v", counts)
	}
}

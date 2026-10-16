package ha

import (
	"fmt"
	"testing"

	"github.com/meister119/leasorix/internal/config"
)

func TestAddressBucketSplitsRangesEvenly(t *testing.T) {
	for _, tc := range []struct {
		start, end uint32
		pct        int
	}{
		{0x0a000a00 + 10, 0x0a000a00 + 250, 50},
		{0x0a000a00 + 10, 0x0a000a00 + 250, 5},
		{0xc0a80000, 0xc0a80000 + 4093, 20},
		{0xc0a80164, 0xc0a80164 + 19, 50},
	} {
		n := int(tc.end - tc.start + 1)
		got := 0
		for ip := tc.start; ip <= tc.end; ip++ {
			if AddressBucket(ip) < tc.pct {
				got++
			}
		}
		want := n * tc.pct / 100
		if d := got - want; d < -2 || d > 2 {
			t.Errorf("range of %d at %d%%: %d addresses, want about %d", n, tc.pct, got, want)
		}
	}
}

func TestClientBucketUniform(t *testing.T) {
	var hist [100]int
	for i := range 20000 {
		mac := fmt.Sprintf("02:00:%02x:%02x:%02x:%02x", i>>24&0xff, i>>16&0xff, i>>8&0xff, i&0xff)
		hist[ClientBucket(mac)]++
	}
	for b, n := range hist {
		if n < 120 || n > 280 {
			t.Fatalf("bucket %d has %d of 20000 clients", b, n)
		}
	}
}

func TestSplitIsComplementary(t *testing.T) {
	for _, mode := range []string{config.HALoadBalance, config.HAHotStandby} {
		pri := split{mode: mode, primary: true, lb: 60, reserve: 5}
		sec := split{mode: mode, primary: false, lb: 60, reserve: 5}
		for ip := uint32(0x0a000000); ip < 0x0a000400; ip++ {
			if pri.owns(ip) == sec.owns(ip) {
				t.Fatalf("%s: address %d owned by both or none", mode, ip)
			}
		}
		for i := range 1000 {
			mac := fmt.Sprintf("02:00:00:00:%02x:%02x", i>>8, i&0xff)
			if mode == config.HALoadBalance && pri.responsible(mac) == sec.responsible(mac) {
				t.Fatalf("client %s answered by both or none", mac)
			}
			if mode == config.HAHotStandby && (!pri.responsible(mac) || sec.responsible(mac)) {
				t.Fatal("hot standby: only the primary answers in NORMAL")
			}
		}
	}
}

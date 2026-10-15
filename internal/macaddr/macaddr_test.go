package macaddr

import (
	"net"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, in := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", "aabb.ccdd.eeff", "AABBCCDDEEFF", " aa:bb:cc:dd:ee:ff "} {
		got, err := Normalize(in)
		if err != nil || got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("Normalize(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "aa:bb", "zz:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff:00"} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) accepted", in)
		}
	}
}

func TestClientID(t *testing.T) {
	got, err := NormalizeClientID("01:AA:BB:CC:DD:EE:FF")
	if err != nil || got != "01aabbccddeeff" {
		t.Fatalf("got %q %v", got, err)
	}
	if got, err := NormalizeClientID(""); err != nil || got != "" {
		t.Fatal("empty client id should be allowed")
	}
	if _, err := NormalizeClientID("0"); err == nil {
		t.Fatal("odd hex accepted")
	}
	if ClientIDFromBytes([]byte{1, 0xaa}) != "01aa" || ClientIDFromBytes(nil) != "" {
		t.Fatal("ClientIDFromBytes")
	}
	if FromHW(net.HardwareAddr{0xAA, 0, 0, 0, 0, 1}) != "aa:00:00:00:00:01" {
		t.Fatal("FromHW")
	}
}

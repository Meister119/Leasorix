// Package macaddr normalises hardware addresses and DHCP client identifiers
// into the canonical textual forms used throughout leasorix:
//
//   - MAC: lower-case, colon separated ("aa:bb:cc:dd:ee:ff")
//   - client identifier (option 61): lower-case hex without separators
package macaddr

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// Normalize parses a MAC address in any common notation
// (aa:bb:.., aa-bb-.., aabb.ccdd.eeff, aabbccddeeff) and returns the
// canonical colon form. Only 6-byte (EUI-48) addresses are accepted.
func Normalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	clean := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s)
	if len(clean) != 12 {
		return "", fmt.Errorf("invalid MAC address %q", s)
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("invalid MAC address %q", s)
	}
	return net.HardwareAddr(b).String(), nil
}

// FromHW formats a hardware address in canonical form. Addresses that are
// not 6 bytes long are formatted as colon separated hex as well.
func FromHW(hw net.HardwareAddr) string {
	return strings.ToLower(hw.String())
}

// NormalizeClientID parses a client identifier given as hex, optionally with
// ":" or "-" separators, and returns lower-case hex without separators.
func NormalizeClientID(s string) (string, error) {
	clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s))
	if clean == "" {
		return "", nil
	}
	b, err := hex.DecodeString(clean)
	if err != nil || len(b) < 2 {
		return "", fmt.Errorf("invalid client identifier %q (expect hex, at least 2 bytes)", s)
	}
	return hex.EncodeToString(b), nil
}

// ClientIDFromBytes encodes a raw option 61 value.
func ClientIDFromBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

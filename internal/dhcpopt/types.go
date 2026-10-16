// Package dhcpopt defines DHCPv4 option types, the catalog of well-known
// options and the conversion between the canonical text form (stored in the
// database, shown in the UI) and the wire encoding.
package dhcpopt

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

// Type is the value type of an option.
type Type string

// Supported option value types.
const (
	TypeIP         Type = "ip"          // single IPv4 address
	TypeIPList     Type = "ip-list"     // one or more IPv4 addresses
	TypeString     Type = "string"      // text
	TypeUint8      Type = "uint8"       // unsigned 8-bit integer
	TypeUint16     Type = "uint16"      // unsigned 16-bit integer
	TypeUint32     Type = "uint32"      // unsigned 32-bit integer
	TypeInt32      Type = "int32"       // signed 32-bit integer
	TypeBool       Type = "bool"        // 1 byte, 0 or 1
	TypeHex        Type = "hex"         // raw bytes given as hex
	TypeDomainList Type = "domain-list" // RFC 1035 encoded names (option 119)
	TypeRoutes     Type = "routes"      // RFC 3442 classless static routes (121, 249)
)

// Types lists all supported types.
var Types = []Type{TypeIP, TypeIPList, TypeString, TypeUint8, TypeUint16, TypeUint32, TypeInt32, TypeBool, TypeHex, TypeDomainList, TypeRoutes}

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// maxValueLen bounds encoded values (long values are split into several
// option instances on the wire, RFC 3396, but must still fit the packet).
const maxValueLen = 1024

// Parse validates a textual value of type t and returns its canonical text
// form and wire encoding.
func Parse(t Type, text string) (canonical string, wire []byte, err error) {
	text = strings.TrimSpace(text)
	switch t {
	case TypeIP:
		a, err := parseIPv4(text)
		if err != nil {
			return "", nil, err
		}
		b := a.As4()
		return a.String(), b[:], nil
	case TypeIPList:
		parts := splitList(text)
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("at least one address required")
		}
		var canon []string
		var wire []byte
		for _, p := range parts {
			a, err := parseIPv4(p)
			if err != nil {
				return "", nil, err
			}
			b := a.As4()
			canon = append(canon, a.String())
			wire = append(wire, b[:]...)
		}
		return strings.Join(canon, ","), wire, checkLen(wire)
	case TypeString:
		if text == "" {
			return "", nil, fmt.Errorf("must not be empty")
		}
		return text, []byte(text), checkLen([]byte(text))
	case TypeUint8, TypeUint16, TypeUint32:
		bits := map[Type]int{TypeUint8: 8, TypeUint16: 16, TypeUint32: 32}[t]
		n, err := strconv.ParseUint(text, 10, bits)
		if err != nil {
			return "", nil, fmt.Errorf("not an unsigned %d-bit number: %q", bits, text)
		}
		wire := make([]byte, bits/8)
		switch bits {
		case 8:
			wire[0] = byte(n)
		case 16:
			binary.BigEndian.PutUint16(wire, uint16(n))
		case 32:
			binary.BigEndian.PutUint32(wire, uint32(n))
		}
		return strconv.FormatUint(n, 10), wire, nil
	case TypeInt32:
		n, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return "", nil, fmt.Errorf("not a signed 32-bit number: %q", text)
		}
		wire := make([]byte, 4)
		binary.BigEndian.PutUint32(wire, uint32(int32(n)))
		return strconv.FormatInt(n, 10), wire, nil
	case TypeBool:
		v, err := strconv.ParseBool(text)
		if err != nil {
			return "", nil, fmt.Errorf("not a boolean: %q", text)
		}
		if v {
			return "true", []byte{1}, nil
		}
		return "false", []byte{0}, nil
	case TypeHex:
		clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(text)
		clean = strings.TrimPrefix(strings.ToLower(clean), "0x")
		b, err := hex.DecodeString(clean)
		if err != nil || len(b) == 0 {
			return "", nil, fmt.Errorf("not a hex byte string: %q", text)
		}
		return hex.EncodeToString(b), b, checkLen(b)
	case TypeDomainList:
		parts := splitList(text)
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("at least one domain required")
		}
		var canon []string
		var wire []byte
		for _, p := range parts {
			enc, name, err := encodeDomain(p)
			if err != nil {
				return "", nil, err
			}
			canon = append(canon, name)
			wire = append(wire, enc...)
		}
		return strings.Join(canon, ","), wire, checkLen(wire)
	case TypeRoutes:
		return parseRoutes(text)
	}
	return "", nil, fmt.Errorf("unknown option type %q", t)
}

func checkLen(b []byte) error {
	if len(b) > maxValueLen {
		return fmt.Errorf("value too long (%d bytes, max %d)", len(b), maxValueLen)
	}
	return nil
}

func splitList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' })
	return f
}

func parseIPv4(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("not an IPv4 address: %q", s)
	}
	return a, nil
}

// encodeDomain encodes a domain name as RFC 1035 labels (no compression).
func encodeDomain(name string) ([]byte, string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || len(name) > 253 {
		return nil, "", fmt.Errorf("invalid domain name %q", name)
	}
	var out []byte
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, "", fmt.Errorf("invalid domain name %q", name)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return nil, "", fmt.Errorf("invalid character %q in domain %q", r, name)
			}
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), name, nil
}

// parseRoutes parses "dest/len gateway" entries separated by commas, e.g.
// "10.0.0.0/8 192.168.1.1, 0.0.0.0/0 192.168.1.254", and encodes them per
// RFC 3442.
func parseRoutes(text string) (string, []byte, error) {
	var canon []string
	var wire []byte
	for _, entry := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		f := strings.Fields(entry)
		if len(f) != 2 {
			return "", nil, fmt.Errorf("route %q: expected \"destination/prefix gateway\"", strings.TrimSpace(entry))
		}
		pfx, err := netip.ParsePrefix(f[0])
		if err != nil || !pfx.Addr().Is4() {
			return "", nil, fmt.Errorf("route %q: invalid destination", entry)
		}
		pfx = pfx.Masked()
		gw, err := parseIPv4(f[1])
		if err != nil {
			return "", nil, fmt.Errorf("route %q: %w", entry, err)
		}
		bits := pfx.Bits()
		dst := pfx.Addr().As4()
		wire = append(wire, byte(bits))
		wire = append(wire, dst[:int(math.Ceil(float64(bits)/8))]...)
		g := gw.As4()
		wire = append(wire, g[:]...)
		canon = append(canon, pfx.String()+" "+gw.String())
	}
	if len(canon) == 0 {
		return "", nil, fmt.Errorf("at least one route required")
	}
	return strings.Join(canon, ", "), wire, checkLen(wire)
}

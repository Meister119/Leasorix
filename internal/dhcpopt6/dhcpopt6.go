// Package dhcpopt6 defines the DHCPv6 option catalog (16-bit codes) and
// the conversion between the canonical text form and the wire encoding.
// Types shared with DHCPv4 (strings, numbers, hex, domain lists) use the
// encoding of package dhcpopt.
package dhcpopt6

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/meister119/leasorix/internal/dhcpopt"
)

// Type is the value type of a DHCPv6 option.
type Type string

// Supported value types.
const (
	TypeIPv6List   Type = "ipv6-list"   // one or more IPv6 addresses
	TypeDomainList Type = "domain-list" // RFC 1035 encoded names
	TypeString     Type = "string"
	TypeUint8      Type = "uint8"
	TypeUint16     Type = "uint16"
	TypeUint32     Type = "uint32"
	TypeHex        Type = "hex"
	// TypeNTPServers encodes addresses as NTP server sub-options
	// (RFC 5908, suboption 1).
	TypeNTPServers Type = "ntp-servers"
)

// Types lists all types.
var Types = []Type{TypeIPv6List, TypeDomainList, TypeString, TypeUint8, TypeUint16, TypeUint32, TypeHex, TypeNTPServers}

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// Definition describes a well-known option.
type Definition struct {
	Code        uint16
	Name        string
	Type        Type
	Description string
}

var builtin = []Definition{
	{21, "sip-server-domains", TypeDomainList, "SIP server domain names (RFC 3319)"},
	{22, "sip-server-addresses", TypeIPv6List, "SIP server addresses (RFC 3319)"},
	{23, "dns-servers", TypeIPv6List, "Recursive DNS servers (RFC 3646)"},
	{24, "domain-search", TypeDomainList, "Domain search list (RFC 3646)"},
	{27, "nis-servers", TypeIPv6List, "NIS servers (RFC 3898)"},
	{28, "nisp-servers", TypeIPv6List, "NIS+ servers (RFC 3898)"},
	{29, "nis-domain", TypeDomainList, "NIS domain name (RFC 3898)"},
	{30, "nisp-domain", TypeDomainList, "NIS+ domain name (RFC 3898)"},
	{31, "sntp-servers", TypeIPv6List, "SNTP servers (RFC 4075)"},
	{32, "information-refresh-time", TypeUint32, "Seconds until stateless clients refresh their configuration (RFC 8415)"},
	{56, "ntp-servers", TypeNTPServers, "NTP servers (RFC 5908)"},
	{59, "bootfile-url", TypeString, "Network boot file URL (RFC 5970)"},
	{64, "aftr-name", TypeDomainList, "DS-Lite AFTR name (RFC 6334)"},
}

// Managed lists the codes the server sets itself or that only clients and
// relays send; they cannot be configured as values.
var Managed = map[uint16]string{
	1:  "client identifier",
	2:  "server identifier",
	3:  "IA_NA",
	4:  "IA_TA",
	5:  "IA address",
	6:  "option request",
	7:  "preference (scope setting)",
	8:  "elapsed time",
	9:  "relay message",
	11: "authentication",
	12: "server unicast",
	13: "status code",
	14: "rapid commit (scope setting)",
	15: "user class",
	16: "vendor class",
	18: "interface id",
	19: "reconfigure message",
	20: "reconfigure accept",
	25: "IA_PD",
	26: "IA prefix",
	39: "client FQDN",
	79: "client link-layer address",
}

var byCode = func() map[uint16]Definition {
	m := make(map[uint16]Definition, len(builtin))
	for _, d := range builtin {
		m[d.Code] = d
	}
	return m
}()

// Builtin returns the definition of a well-known option.
func Builtin(code uint16) (Definition, bool) {
	d, ok := byCode[code]
	return d, ok
}

// Definitions returns the well-known options ordered by code.
func Definitions() []Definition {
	out := append([]Definition(nil), builtin...)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

const maxValueLen = 1024

// Parse validates a value and returns its canonical text and wire form.
func Parse(t Type, text string) (string, []byte, error) {
	text = strings.TrimSpace(text)
	switch t {
	case TypeIPv6List, TypeNTPServers:
		parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' })
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("at least one address required")
		}
		var canon []string
		var wire []byte
		for _, p := range parts {
			a, err := netip.ParseAddr(p)
			if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
				return "", nil, fmt.Errorf("not an IPv6 address: %q", p)
			}
			b := a.As16()
			if t == TypeNTPServers {
				wire = binary.BigEndian.AppendUint16(wire, 1) // NTP_SUBOPTION_SRV_ADDR
				wire = binary.BigEndian.AppendUint16(wire, 16)
			}
			wire = append(wire, b[:]...)
			canon = append(canon, a.String())
		}
		if len(wire) > maxValueLen {
			return "", nil, fmt.Errorf("value too long (%d bytes, max %d)", len(wire), maxValueLen)
		}
		return strings.Join(canon, ","), wire, nil
	case TypeDomainList:
		return dhcpopt.Parse(dhcpopt.TypeDomainList, text)
	case TypeString:
		return dhcpopt.Parse(dhcpopt.TypeString, text)
	case TypeUint8:
		return dhcpopt.Parse(dhcpopt.TypeUint8, text)
	case TypeUint16:
		return dhcpopt.Parse(dhcpopt.TypeUint16, text)
	case TypeUint32:
		return dhcpopt.Parse(dhcpopt.TypeUint32, text)
	case TypeHex:
		return dhcpopt.Parse(dhcpopt.TypeHex, text)
	}
	return "", nil, fmt.Errorf("unknown option type %q", t)
}

// TypeFor returns the type to use for code: the built-in one, or the
// given type for other codes. It rejects managed codes.
func TypeFor(code uint16, given Type) (Type, error) {
	if why, ok := Managed[code]; ok || code == 0 {
		return "", fmt.Errorf("option %d cannot be configured (%s)", code, why)
	}
	if d, ok := byCode[code]; ok {
		if given != "" && given != d.Type {
			return "", fmt.Errorf("option %d (%s) has type %s", code, d.Name, d.Type)
		}
		return d.Type, nil
	}
	if !given.Valid() {
		return "", fmt.Errorf("option %d is not well known: give a type (%v)", code, Types)
	}
	return given, nil
}

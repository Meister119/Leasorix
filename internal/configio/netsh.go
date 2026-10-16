package configio

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/macaddr"
	"github.com/meister119/leasorix/internal/scope"
)

// Warning is a line of the netsh dump that was not (fully) imported.
type Warning struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

const maxWarnings = 500

// windowsDefaultClasses are built into every Windows DHCP server; they are
// not imported.
var windowsDefaultClasses = map[string]bool{
	"default routing and remote access class":   true,
	"default bootp class":                       true,
	"default network access protection class":   true,
	"microsoft windows 2000 options":            true,
	"microsoft windows 98 options":              true,
	"microsoft options":                         true,
	"default user class":                        true,
	"default vendor class":                      true,
	"microsoft windows 2000 options (vendor)":   true,
	"microsoft network access protection class": true,
	"default network access protection classes": true,
}

type netshParser struct {
	doc      *Document
	warnings []Warning
	scopes   map[string]*scope.Bundle // by network address
	order    []string
	ranges   map[string]bool
	defs     map[uint8]dhcpopt.Type // custom definitions of the dump
	supers   map[string]bool
	settings Settings
	setSet   bool
	skipped  map[string]int
	line     int
	text     string
}

// ParseNetsh converts the output of "netsh dhcp server dump" (Windows DHCP
// server) into a configuration document. Scopes, ranges, exclusions,
// reservations, option values (server, scope, reservation), custom option
// definitions, classes, superscopes and MAC filters are converted; other
// settings are reported as warnings.
func ParseNetsh(r io.Reader) (*Document, []Warning, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	r = bytes.NewReader(decodeText(raw))
	p := &netshParser{
		doc:     &Document{Format: Format},
		scopes:  map[string]*scope.Bundle{},
		ranges:  map[string]bool{},
		defs:    map[uint8]dhcpopt.Type{},
		supers:  map[string]bool{},
		skipped: map[string]int{},
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		p.line++
		p.text = strings.TrimSpace(strings.TrimPrefix(sc.Text(), string(rune(0xfeff))))
		if p.text == "" || strings.HasPrefix(p.text, "#") || strings.HasPrefix(p.text, ";") {
			continue
		}
		toks := tokenize(p.text)
		if len(toks) < 3 || !strings.EqualFold(toks[0], "dhcp") || !strings.EqualFold(toks[1], "server") {
			if strings.EqualFold(firstWord(p.text), "pushd") || strings.EqualFold(firstWord(p.text), "popd") {
				continue
			}
			p.warn("not a netsh DHCP server command")
			continue
		}
		p.command(toks[3:])
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	for net := range p.scopes {
		if !p.ranges[net] {
			p.warnAt(0, "scope "+net, "scope has no address range (add iprange); skipped")
		}
	}
	for _, net := range p.order {
		if p.ranges[net] {
			p.doc.Scopes = append(p.doc.Scopes, *p.scopes[net])
		}
	}
	if p.setSet {
		s := p.settings
		p.doc.Settings = &s
	}
	reasons := make([]string, 0, len(p.skipped))
	for reason := range p.skipped {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		n := p.skipped[reason]
		text := fmt.Sprintf("%d lines", n)
		if n == 1 {
			text = "1 line"
		}
		p.warnAt(0, text, reason)
	}
	if len(p.doc.Scopes) == 0 && len(p.doc.ServerOptions) == 0 && len(p.doc.Classes) == 0 && len(p.doc.OptionDefinitions) == 0 {
		return nil, p.warnings, invalid("no importable DHCP configuration found (is this the output of 'netsh dhcp server dump'?)")
	}
	return p.doc, p.warnings, nil
}

// decodeText converts UTF-16 (PowerShell redirection) to UTF-8.
func decodeText(b []byte) []byte {
	if len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		le := b[0] == 0xff
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			if le {
				u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
			} else {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			}
		}
		return []byte(string(utf16.Decode(u)))
	}
	return b
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func (p *netshParser) warn(reason string) { p.warnAt(p.line, p.text, reason) }

func (p *netshParser) warnAt(line int, text, reason string) {
	if len(p.warnings) < maxWarnings {
		p.warnings = append(p.warnings, Warning{Line: line, Text: text, Reason: reason})
	}
}

// tokenize splits a line at blanks; double quotes group (and are removed),
// also inside key="value" tokens.
func tokenize(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote bool
		have  bool
	)
	for _, r := range s {
		switch {
		case r == '"':
			quote = !quote
			have = true
		case unicode.IsSpace(r) && !quote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func lower(s string) string { return strings.ToLower(s) }

func (p *netshParser) command(t []string) {
	if len(t) == 0 {
		p.warn("empty command")
		return
	}
	switch lower(t[0]) {
	case "add":
		if len(t) < 2 {
			p.warn("incomplete command")
			return
		}
		switch lower(t[1]) {
		case "scope":
			p.addScope(t[2:])
		case "optiondef":
			p.addOptionDef(t[2:])
		case "class":
			p.addClass(t[2:])
		case "filter":
			p.addFilter(t[2:])
		case "mscope":
			p.skipped["multicast scopes (MADCAP) are not supported"]++
		default:
			p.warn("unsupported server command")
		}
	case "set":
		if len(t) < 2 {
			p.warn("incomplete command")
			return
		}
		switch lower(t[1]) {
		case "optionvalue":
			p.optionValue(t[2:], func(o scope.BundleOption) { p.doc.ServerOptions = append(p.doc.ServerOptions, o) }, "server")
		case "filter":
			p.setFilter(t[2:])
		case "dnsconfig":
			p.warn("dynamic DNS settings are configured separately in Leasorix (phase 9)")
		default:
			p.skipped["server settings without Leasorix equivalent (audit log, database, bindings, ...)"]++
		}
	case "scope":
		if len(t) < 3 {
			p.warn("incomplete scope command")
			return
		}
		p.scopeCommand(t[1], t[2:])
	case "mscope":
		p.skipped["multicast scopes (MADCAP) are not supported"]++
	case "v6":
		p.skipped["DHCPv6 configuration is not imported"]++
	case "initiate", "delete", "export", "import":
		p.warn("not a configuration command")
	default:
		p.skipped["server settings without Leasorix equivalent (audit log, database, bindings, ...)"]++
	}
}

func maskBits(mask string) (int, bool) {
	a, err := netip.ParseAddr(mask)
	if err != nil || !a.Is4() {
		return 0, false
	}
	b := a.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	bits := 0
	for v&0x80000000 != 0 {
		bits++
		v <<= 1
	}
	return bits, v == 0
}

func (p *netshParser) addScope(t []string) {
	if len(t) < 2 {
		p.warn("add scope: network and mask required")
		return
	}
	bits, ok := maskBits(t[1])
	netAddr, err := netip.ParseAddr(t[0])
	if !ok || err != nil || !netAddr.Is4() {
		p.warn("add scope: invalid network or mask")
		return
	}
	prefix := netip.PrefixFrom(netAddr, bits).Masked()
	name := t[0]
	if len(t) > 2 && t[2] != "" {
		name = t[2]
	}
	b := &scope.Bundle{
		Scope:        scope.BundleScope{Name: name, Subnet: prefix.String(), LeaseTime: 8 * 24 * 3600, Enabled: true},
		Exclusions:   []scope.BundleRange{},
		Reservations: []scope.BundleReservation{},
		Options:      []scope.BundleOption{},
		Policies:     []scope.BundlePolicy{},
	}
	if len(t) > 3 {
		b.Scope.Description = t[3]
	}
	key := netAddr.String()
	if _, dup := p.scopes[key]; dup {
		p.warn("scope defined twice; later definition wins")
	} else {
		p.order = append(p.order, key)
	}
	p.scopes[key] = b
}

func (p *netshParser) scopeCommand(net string, t []string) {
	b := p.scopes[net]
	if b == nil {
		p.warn("scope " + net + " was not defined with 'add scope'")
		return
	}
	verb := lower(t[0])
	rest := t[1:]
	if len(rest) == 0 {
		p.warn("incomplete scope command")
		return
	}
	obj := lower(rest[0])
	switch {
	case verb == "add" && obj == "iprange" && len(rest) >= 3:
		b.Scope.Start, b.Scope.End = rest[1], rest[2]
		p.ranges[net] = true
		if len(rest) > 3 && (strings.EqualFold(rest[3], "BOOTP") || strings.EqualFold(rest[3], "BOTH")) {
			b.Scope.BOOTP = true
		}
	case verb == "add" && obj == "excluderange" && len(rest) >= 3:
		b.Exclusions = append(b.Exclusions, scope.BundleRange{Start: rest[1], End: rest[2]})
	case verb == "add" && obj == "reservedip" && len(rest) >= 3:
		p.addReservation(b, rest[1:])
	case verb == "reservedip" && len(rest) >= 3 && lower(rest[1]) == "set" && lower(rest[2]) == "optionvalue":
		ip := rest[0]
		for i := range b.Reservations {
			if b.Reservations[i].IP == ip {
				r := &b.Reservations[i]
				p.optionValue(rest[3:], func(o scope.BundleOption) { r.Options = append(r.Options, o) }, "reservation")
				return
			}
		}
		p.warn("option for an unknown reservation")
	case verb == "set" && obj == "optionvalue":
		p.optionValue(rest[1:], func(o scope.BundleOption) {
			if o.Code == 51 {
				return
			}
			b.Options = append(b.Options, o)
		}, "scope")
		if len(rest) > 3 && rest[1] == "51" {
			if n, err := strconv.ParseUint(rest[len(rest)-1], 10, 32); err == nil {
				if n == 0xffffffff {
					n = 0
				}
				b.Scope.LeaseTime = int64(n)
			}
		}
	case verb == "set" && obj == "state" && len(rest) >= 2:
		b.Scope.Enabled = rest[1] == "1" || rest[1] == "3"
	case verb == "set" && obj == "name" && len(rest) >= 2:
		b.Scope.Name = rest[1]
	case verb == "set" && obj == "comment" && len(rest) >= 2:
		b.Scope.Description = rest[1]
	case verb == "set" && obj == "superscope" && len(rest) >= 2:
		b.Scope.Superscope = rest[1]
		if !p.supers[lower(rest[1])] {
			p.supers[lower(rest[1])] = true
			p.doc.Superscopes = append(p.doc.Superscopes, Superscope{Name: rest[1]})
		}
	case verb == "set" && obj == "dnsconfig":
		p.warn("dynamic DNS settings are configured separately in Leasorix (phase 9)")
	case verb == "add" && obj == "policy", verb == "set" && obj == "policy":
		p.warn("Windows policies are not converted; create them in Leasorix")
	default:
		p.skipped["scope settings without Leasorix equivalent (delay offer, NAP, ...)"]++
	}
}

func (p *netshParser) addReservation(b *scope.Bundle, t []string) {
	ip, uid := t[0], strings.ToLower(strings.NewReplacer("-", "", ":", "").Replace(t[1]))
	if _, err := hex.DecodeString(uid); err != nil || uid == "" {
		p.warn("reservation with invalid client identifier")
		return
	}
	r := scope.BundleReservation{IP: ip}
	if len(uid) == 12 {
		mac, err := macaddr.Normalize(uid)
		if err != nil {
			p.warn("reservation with invalid MAC address")
			return
		}
		r.MAC = mac
	} else {
		r.ClientID = uid
	}
	if len(t) > 2 {
		r.Hostname = t[2]
	}
	if len(t) > 3 {
		r.Description = t[3]
	}
	b.Reservations = append(b.Reservations, r)
}

// optionValue converts "<code> <TYPE> [user=..] [vendor=..] values..." and
// validates the value against the option's type.
func (p *netshParser) optionValue(t []string, add func(scope.BundleOption), level string) {
	if len(t) < 2 {
		p.warn("incomplete optionvalue")
		return
	}
	code64, err := strconv.ParseUint(t[0], 10, 8)
	if err != nil || code64 == 0 || code64 == 255 {
		p.warn("invalid option code")
		return
	}
	code := uint8(code64)
	typ := strings.ToUpper(t[1])
	var values []string
	for _, v := range t[2:] {
		lv := lower(v)
		if strings.HasPrefix(lv, "user=") || strings.HasPrefix(lv, "vendor=") {
			if strings.TrimSpace(v[strings.IndexByte(v, '=')+1:]) != "" {
				p.warn("class-specific option values are not converted (use a Leasorix policy)")
				return
			}
			continue
		}
		values = append(values, v)
	}
	if code == 51 && level == "scope" {
		return // becomes the scope lease time
	}
	if _, managed := dhcpopt.Managed[code]; managed {
		p.warn(fmt.Sprintf("option %d is managed by Leasorix and not imported", code))
		return
	}
	if len(values) == 0 {
		p.warn("option without value")
		return
	}
	var text string
	switch typ {
	case "IPADDRESS":
		text = strings.Join(values, ",")
	case "STRING", "BYTE", "WORD", "DWORD":
		if len(values) > 1 {
			text = strings.Join(values, ",")
		} else {
			text = values[0]
		}
	case "BINARY", "ENCAPSULATED":
		text = strings.Join(values, "")
	default:
		p.warn("option type " + typ + " is not supported")
		return
	}
	lt, ok := p.defs[code]
	if !ok {
		def, builtin := dhcpopt.Builtin(code)
		if !builtin {
			p.warn(fmt.Sprintf("option %d has no definition", code))
			return
		}
		lt = def.Type
	}
	canonical, _, err := dhcpopt.Parse(lt, text)
	if err != nil {
		p.warn(fmt.Sprintf("option %d: %v", code, err))
		return
	}
	add(scope.BundleOption{Code: code, Value: canonical})
}

func (p *netshParser) addOptionDef(t []string) {
	if len(t) < 4 {
		p.warn("incomplete optiondef")
		return
	}
	code64, err := strconv.ParseUint(t[0], 10, 8)
	if err != nil || code64 == 0 || code64 == 255 {
		p.warn("invalid option code")
		return
	}
	code := uint8(code64)
	for _, x := range t[4:] {
		if strings.HasPrefix(lower(x), "vendor=") && len(x) > len("vendor=") {
			p.warn("vendor-specific option definitions are not converted")
			return
		}
	}
	if _, builtin := dhcpopt.Builtin(code); builtin {
		return // standard option, already known
	}
	if _, managed := dhcpopt.Managed[code]; managed {
		return // standard option, already known
	}
	array := t[3] == "1"
	var lt dhcpopt.Type
	switch strings.ToUpper(t[2]) {
	case "IPADDRESS":
		lt = dhcpopt.TypeIP
		if array {
			lt = dhcpopt.TypeIPList
		}
	case "STRING":
		lt = dhcpopt.TypeString
	case "BYTE":
		lt = dhcpopt.TypeUint8
	case "WORD":
		lt = dhcpopt.TypeUint16
	case "DWORD":
		lt = dhcpopt.TypeUint32
	case "BINARY", "ENCAPSULATED":
		lt = dhcpopt.TypeHex
	default:
		p.warn("option definition type " + t[2] + " is not supported")
		return
	}
	if array && lt != dhcpopt.TypeIPList {
		lt = dhcpopt.TypeHex
		p.warn(fmt.Sprintf("array option %d imported as hex", code))
	}
	desc := ""
	for _, x := range t[4:] {
		if strings.HasPrefix(lower(x), "comment=") {
			desc = x[len("comment="):]
		}
	}
	p.defs[code] = lt
	p.doc.OptionDefinitions = append(p.doc.OptionDefinitions, OptionDefinition{Code: code, Name: t[1], Type: string(lt), Description: desc})
}

func (p *netshParser) addClass(t []string) {
	if len(t) < 4 {
		p.warn("incomplete class definition")
		return
	}
	name, comment, data := t[0], t[1], t[2]
	if windowsDefaultClasses[lower(name)] {
		return
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(lower(data), "0x"))
	if err != nil || len(raw) == 0 {
		p.warn("class with invalid data")
		return
	}
	for _, c := range raw {
		if c < 0x20 || c > 0x7e {
			p.warn("class data is not printable text; not converted")
			return
		}
	}
	typ := "user"
	if t[3] == "1" {
		typ = "vendor"
	}
	p.doc.Classes = append(p.doc.Classes, Class{Name: name, Type: typ, Data: string(raw), Description: comment})
}

func (p *netshParser) addFilter(t []string) {
	if len(t) < 2 || (lower(t[0]) != "allow" && lower(t[0]) != "deny") {
		p.warn("filter: allow or deny and a MAC address required")
		return
	}
	pattern, err := scope.NormalizeMACPattern(strings.ReplaceAll(t[1], "-", ":"))
	if err != nil {
		p.warn("filter: invalid MAC address")
		return
	}
	f := MACFilter{List: lower(t[0]), Pattern: pattern}
	if len(t) > 2 {
		f.Description = t[2]
	}
	p.doc.MACFilters = append(p.doc.MACFilters, f)
}

func (p *netshParser) setFilter(t []string) {
	for _, kv := range t {
		k, v, ok := strings.Cut(lower(kv), "=")
		if !ok {
			continue
		}
		switch k {
		case "enforceallowlist":
			p.settings.MACFilterAllowEnabled = v == "1"
			p.setSet = true
		case "enforcedenylist":
			p.settings.MACFilterDenyEnabled = v == "1"
			p.setSet = true
		}
	}
}

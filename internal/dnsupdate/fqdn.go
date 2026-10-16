package dnsupdate

import (
	"errors"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/scope"
)

// Client FQDN option flags (RFC 4702 section 2.1).
const (
	flagS = 0x01 // server performs the A record update
	flagO = 0x02 // server overrode the client's S bit
	flagE = 0x04 // name in canonical wire format
	flagN = 0x08 // server performs no updates
)

// ClientFQDN is a parsed Client FQDN option (81).
type ClientFQDN struct {
	// S: the client asks the server to update the A record.
	S bool
	// N: the client asks the server not to update anything.
	N bool
	// E: the name is in DNS wire format (otherwise ASCII, deprecated).
	E bool
	// Name is the name as sent by the client (lower case, without
	// trailing dot); it may be a single label.
	Name string
}

// ParseClientFQDN decodes the value of option 81.
func ParseClientFQDN(b []byte) (*ClientFQDN, error) {
	if len(b) < 3 {
		return nil, errors.New("client FQDN option too short")
	}
	f := &ClientFQDN{S: b[0]&flagS != 0, N: b[0]&flagN != 0, E: b[0]&flagE != 0}
	raw := b[3:]
	if !f.E {
		f.Name = strings.Trim(strings.ToLower(strings.TrimRight(string(raw), "\x00")), ".")
		return f, nil
	}
	var labels []string
	for rest := raw; len(rest) > 0; {
		n := int(rest[0])
		rest = rest[1:]
		if n == 0 {
			break
		}
		if n > 63 || n > len(rest) {
			return nil, errors.New("client FQDN option: malformed name")
		}
		labels = append(labels, strings.ToLower(string(rest[:n])))
		rest = rest[n:]
	}
	f.Name = strings.Join(labels, ".")
	return f, nil
}

// encodeName returns name (without trailing dot) as fully qualified wire
// format or ASCII.
func encodeName(name string, wire bool) []byte {
	if !wire {
		return []byte(name)
	}
	var out []byte
	if name != "" {
		for _, l := range strings.Split(name, ".") {
			out = append(out, byte(len(l)))
			out = append(out, l...)
		}
	}
	return append(out, 0)
}

// HostLabel turns a client supplied host name into a DNS label: the first
// label, lower case, characters other than letters, digits and hyphens
// replaced by hyphens, at most 63 characters, no leading or trailing
// hyphen. It returns "" if nothing usable is left.
func HostLabel(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 63; i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b = append(b, c+'a'-'A')
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	return strings.Trim(string(b), "-")
}

// Decision is what the server does in DNS for one binding and how it
// answers the Client FQDN option.
type Decision struct {
	// FQDN is the name to register (without trailing dot), "" if none.
	FQDN string
	// Forward: the server registers the A record (with DHCID when the
	// scope protects names).
	Forward bool
	// Reverse: the server registers the PTR record.
	Reverse bool
	// Reply is the Client FQDN option for OFFER/ACK; nil when the client
	// did not send one.
	Reply []byte
}

// Updates reports whether any record is to be registered.
func (d Decision) Updates() bool { return d.FQDN != "" && (d.Forward || d.Reverse) }

// Decide applies the scope settings to a client's Client FQDN option (raw
// value, nil if absent), its host name option (12) and the domain name
// option (15) it receives.
//
// The registered name is always the client's first label below the scope
// domain (or option 15), so a client cannot claim a name in another zone.
func Decide(s scope.DNSSettings, fqdnOpt []byte, hostname, optionDomain string) Decision {
	var cf *ClientFQDN
	if fqdnOpt != nil {
		cf, _ = ParseClientFQDN(fqdnOpt)
	}
	label := ""
	if cf != nil {
		label = HostLabel(cf.Name)
	}
	if label == "" {
		label = HostLabel(hostname)
	}
	domain := s.Domain
	if domain == "" {
		domain = scope.NormalizeDNSName(optionDomain)
	}
	var d Decision
	if label != "" && domain != "" && scope.ValidDNSName(label+"."+domain) {
		d.FQDN = label + "." + domain
	}
	if d.FQDN != "" {
		switch s.Mode {
		case scope.DNSAlways:
			d.Forward, d.Reverse = true, true
		case scope.DNSClient:
			if cf != nil && !cf.N {
				d.Forward, d.Reverse = cf.S, true
			}
		}
	}
	if cf != nil {
		d.Reply = reply(cf, d)
	}
	return d
}

// reply builds the server's Client FQDN option (RFC 4702 section 4):
// S = the server updates the A record, O = that differs from the client's
// request, N = the server updates nothing; RCODE1/2 = 255.
func reply(cf *ClientFQDN, d Decision) []byte {
	var flags byte
	if d.Forward {
		flags |= flagS
	}
	if d.Forward != cf.S {
		flags |= flagO
	}
	if !d.Forward && !d.Reverse {
		flags |= flagN
	}
	if cf.E {
		flags |= flagE
	}
	name := d.FQDN
	if name == "" {
		name = cf.Name
	}
	return append([]byte{flags, 255, 255}, encodeName(name, cf.E)...)
}

// RecordTTL is the TTL used for the records of a lease: the configured
// value, or a third of the lease time between one minute and one day
// (unlimited leases: one day).
func RecordTTL(s scope.DNSSettings, leaseTime time.Duration) uint32 {
	if s.TTL > 0 {
		return uint32(s.TTL / time.Second)
	}
	ttl := leaseTime / 3
	if leaseTime <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	return uint32(ttl / time.Second)
}

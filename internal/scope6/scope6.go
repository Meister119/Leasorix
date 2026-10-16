// Package scope6 stores the DHCPv6 configuration (scopes, exclusions,
// reservations, options) and keeps an immutable snapshot for the DHCPv6
// engine, like package scope does for IPv4.
package scope6

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
)

// Limits.
const (
	MinPrefixLen = 16
	MaxPrefixLen = 127
	// MaxDelegatedLen is the longest delegated prefix.
	MaxDelegatedLen = 64
	// MinValidLifetime is the shortest valid lifetime.
	MinValidLifetime = time.Minute
	// MaxLifetime is the longest finite lifetime (2^32-2 seconds).
	MaxLifetime   = (1<<32 - 2) * time.Second
	maxNameLen    = 128
	maxPDPrefixes = 63 // delegated length - pool length
)

// Scope is a DHCPv6 scope: the prefix of a link with an optional address
// range (IA_NA) and an optional prefix delegation pool (IA_PD).
type Scope struct {
	ID          int64
	Name        string
	Description string
	// Prefix is the on-link prefix, e.g. 2001:db8:1::/64.
	Prefix netip.Prefix
	// Start and End delimit the address range; both zero = no addresses.
	Start, End netip.Addr
	// PreferredLifetime and ValidLifetime of addresses and prefixes.
	PreferredLifetime time.Duration
	ValidLifetime     time.Duration
	// RapidCommit allows two-message exchanges (SOLICIT -> REPLY) when
	// the client asks for it.
	RapidCommit bool
	// Preference is sent in ADVERTISE (option 7).
	Preference uint8
	Enabled    bool
	// PDPool is the prefix delegation pool (zero = none) and PDLength the
	// length of the delegated prefixes.
	PDPool    netip.Prefix
	PDLength  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasAddresses reports whether the scope assigns addresses.
func (s *Scope) HasAddresses() bool { return s.Start.IsValid() }

// HasPD reports whether the scope delegates prefixes.
func (s *Scope) HasPD() bool { return s.PDPool.IsValid() }

// usable reports whether a is a global or unique local unicast address.
func usable(a netip.Addr) bool {
	return a.Is6() && !a.Is4In6() && a.Zone() == "" && !a.IsLinkLocalUnicast() && !a.IsMulticast() && !a.IsLoopback() && !a.IsUnspecified()
}

func checkPrefix(v *apperr.Validator, field string, p netip.Prefix, minLen, maxLen int) bool {
	if !p.IsValid() || !usable(p.Addr()) {
		v.Add(field, "must be a global or unique local IPv6 prefix like 2001:db8:1::/64")
		return false
	}
	if p.Masked() != p {
		v.Add(field, "host bits set; use %s", p.Masked())
		return false
	}
	if p.Bits() < minLen || p.Bits() > maxLen {
		v.Add(field, "prefix length must be between /%d and /%d", minLen, maxLen)
		return false
	}
	return true
}

func (s *Scope) validate() error {
	var v apperr.Validator
	s.Name = strings.TrimSpace(s.Name)
	v.Check(s.Name != "" && len(s.Name) <= maxNameLen, "name", "must be 1-%d characters", maxNameLen)
	v.Check(len(s.Description) <= 1024, "description", "must be at most 1024 characters")
	okPrefix := checkPrefix(&v, "prefix", s.Prefix, MinPrefixLen, MaxPrefixLen)
	switch {
	case s.Start.IsValid() != s.End.IsValid():
		v.Add("end", "give both start and end, or neither (no address assignment)")
	case s.Start.IsValid():
		v.Check(usable(s.Start), "start", "must be an IPv6 address")
		v.Check(usable(s.End), "end", "must be an IPv6 address")
		if okPrefix {
			v.Check(s.Prefix.Contains(s.Start), "start", "%s is not in %s", s.Start, s.Prefix)
			v.Check(s.Prefix.Contains(s.End), "end", "%s is not in %s", s.End, s.Prefix)
		}
		v.Check(s.Start.Compare(s.End) <= 0, "end", "must not be lower than start")
	}
	if s.PDPool.IsValid() {
		if checkPrefix(&v, "pd_pool", s.PDPool, MinPrefixLen, MaxDelegatedLen-1) {
			v.Check(s.PDLength > s.PDPool.Bits() && s.PDLength <= MaxDelegatedLen && s.PDLength-s.PDPool.Bits() <= maxPDPrefixes,
				"pd_length", "must be longer than the pool (/%d) and at most /%d", s.PDPool.Bits(), MaxDelegatedLen)
			if okPrefix {
				v.Check(!s.PDPool.Overlaps(s.Prefix), "pd_pool", "must not overlap the link prefix")
			}
		}
	} else {
		v.Check(s.PDLength == 0, "pd_length", "requires a delegation pool")
	}
	v.Check(s.Start.IsValid() || s.PDPool.IsValid(), "start", "the scope must assign addresses, delegate prefixes or both")
	v.Check(s.ValidLifetime >= MinValidLifetime && s.ValidLifetime <= MaxLifetime && s.ValidLifetime%time.Second == 0,
		"valid_lifetime", "must be between %d and %d seconds", int64(MinValidLifetime/time.Second), int64(MaxLifetime/time.Second))
	v.Check(s.PreferredLifetime > 0 && s.PreferredLifetime <= s.ValidLifetime && s.PreferredLifetime%time.Second == 0,
		"preferred_lifetime", "must be positive and not longer than the valid lifetime")
	return v.Err()
}

// Exclusion removes an address range of a scope from dynamic assignment.
type Exclusion struct {
	ID         int64
	ScopeID    int64
	Start, End netip.Addr
}

// AnyIAID matches every IA of the client in a reservation.
const AnyIAID = -1

// Reservation binds a fixed address to a client DUID (and optionally one
// IAID).
type Reservation struct {
	ID      int64
	ScopeID int64
	// DUID is lower-case hex without separators.
	DUID string
	// IAID is the identity association, or AnyIAID.
	IAID        int64
	IP          netip.Addr
	Hostname    string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NormalizeDUID parses a DUID given as hex (with or without ":"/"-").
func NormalizeDUID(s string) (string, error) {
	clean := strings.ToLower(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s)))
	b, err := hex.DecodeString(clean)
	if err != nil || len(b) < 3 || len(b) > 130 {
		return "", errors.New("must be 3-130 bytes as hex, e.g. 00:01:00:01:2b:3c:4d:5e:02:00:00:00:00:01")
	}
	return clean, nil
}

func (r *Reservation) validate(sc *Scope) error {
	var v apperr.Validator
	d, err := NormalizeDUID(r.DUID)
	if err != nil {
		v.Add("duid", "%s", err.Error())
	}
	r.DUID = d
	v.Check(r.IAID >= AnyIAID && r.IAID <= 1<<32-1, "iaid", "must be -1 (any) or 0-4294967295")
	v.Check(r.IP.IsValid() && usable(r.IP) && sc.Prefix.Contains(r.IP), "ip", "must be an address in %s", sc.Prefix)
	r.Hostname = strings.TrimSpace(r.Hostname)
	v.Check(len(r.Hostname) <= 253, "hostname", "too long")
	v.Check(len(r.Description) <= 1024, "description", "too long")
	return v.Err()
}

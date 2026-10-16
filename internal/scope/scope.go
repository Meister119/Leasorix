package scope

import (
	"net/netip"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/ip4"
)

// Limits for scope definitions.
const (
	MinPrefixLen = 8
	MaxPrefixLen = 30
	// MinLeaseTime is the shortest configurable lease (0 = unlimited).
	MinLeaseTime = 10 * time.Second
	maxNameLen   = 128
)

// Scope is an IPv4 scope: a subnet with an address range handed out
// dynamically.
type Scope struct {
	ID          int64
	Name        string
	Description string
	// Subnet is the network of the scope, e.g. 192.168.1.0/24.
	Subnet netip.Prefix
	// Start and End delimit the dynamic address range (inclusive).
	Start, End netip.Addr
	// LeaseTime is the lease duration; 0 means unlimited.
	LeaseTime time.Duration
	Enabled   bool
	// BOOTP enables dynamic BOOTP for this scope (reservations always
	// answer BOOTP clients).
	BOOTP bool
	// SuperscopeID is the superscope the scope belongs to (0 = none).
	SuperscopeID int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Exclusion removes an address range from dynamic allocation.
type Exclusion struct {
	ID      int64
	ScopeID int64
	Start   netip.Addr
	End     netip.Addr
}

// Reservation binds a fixed address to a client, identified by MAC address
// and/or DHCP client identifier (option 61).
type Reservation struct {
	ID          int64
	ScopeID     int64
	IP          netip.Addr
	MAC         string // canonical aa:bb:cc:dd:ee:ff or ""
	ClientID    string // lower-case hex or ""
	Hostname    string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// validate checks a scope in isolation (no cross-scope checks).
func (s *Scope) validate() error {
	var v apperr.Validator
	s.Name = strings.TrimSpace(s.Name)
	v.Check(s.Name != "", "name", "must not be empty")
	v.Check(len(s.Name) <= maxNameLen, "name", "must be at most %d characters", maxNameLen)

	if !s.Subnet.IsValid() || !s.Subnet.Addr().Is4() {
		v.Add("subnet", "must be an IPv4 prefix like 192.168.1.0/24")
		return v.Err()
	}
	bits := s.Subnet.Bits()
	if bits < MinPrefixLen || bits > MaxPrefixLen {
		v.Add("subnet", "prefix length must be between /%d and /%d", MinPrefixLen, MaxPrefixLen)
		return v.Err()
	}
	if s.Subnet.Masked() != s.Subnet {
		v.Add("subnet", "host bits set; use the network address %s", s.Subnet.Masked())
		return v.Err()
	}
	network := ip4.MustFromAddr(s.Subnet.Addr())
	bcast := ip4.Broadcast(network, bits)

	start, okS := ip4.FromAddr(s.Start)
	end, okE := ip4.FromAddr(s.End)
	v.Check(okS, "start", "must be an IPv4 address")
	v.Check(okE, "end", "must be an IPv4 address")
	if okS && okE {
		v.Check(ip4.Contains(network, bits, start), "start", "%s is not in subnet %s", s.Start, s.Subnet)
		v.Check(ip4.Contains(network, bits, end), "end", "%s is not in subnet %s", s.End, s.Subnet)
		v.Check(start != network && start != bcast, "start", "must not be the network or broadcast address")
		v.Check(end != network && end != bcast, "end", "must not be the network or broadcast address")
		v.Check(start <= end, "end", "must not be lower than start")
	}
	v.Check(s.LeaseTime == 0 || s.LeaseTime >= MinLeaseTime, "lease_time", "must be 0 (unlimited) or at least %s", MinLeaseTime)
	v.Check(s.LeaseTime%time.Second == 0, "lease_time", "must be a whole number of seconds")
	return v.Err()
}

// Range returns the dynamic range as integers.
func (s *Scope) Range() ip4.Range {
	return ip4.Range{Start: ip4.MustFromAddr(s.Start), End: ip4.MustFromAddr(s.End)}
}

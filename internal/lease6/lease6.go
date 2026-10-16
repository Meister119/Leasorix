// Package lease6 manages DHCPv6 bindings: addresses (IA_NA) and delegated
// prefixes (IA_PD). The table lives in memory and is persisted in leases6
// before a binding is confirmed to the client.
package lease6

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"time"
)

// Kind of binding.
type Kind string

// Binding kinds.
const (
	KindNA Kind = "na" // address
	KindPD Kind = "pd" // delegated prefix
)

// State of a binding.
type State string

// Binding states.
const (
	StateActive   State = "active"
	StateExpired  State = "expired"
	StateReleased State = "released"
	StateDeclined State = "declined"
)

// Lease is a DHCPv6 binding.
type Lease struct {
	// Prefix is the address (/128) or the delegated prefix.
	Prefix  netip.Prefix
	Kind    Kind
	ScopeID int64
	// DUID of the client (lower-case hex) and its IA.
	DUID     string
	IAID     uint32
	Hostname string
	State    State
	Start    time.Time
	// Preferred and Expires are the ends of the preferred and valid
	// lifetimes.
	Preferred time.Time
	Expires   time.Time
	Updated   time.Time
}

// Addr returns the address (or the first address of the prefix).
func (l *Lease) Addr() netip.Addr { return l.Prefix.Addr() }

// ActiveAt reports whether the binding holds its address at t.
func (l *Lease) ActiveAt(t time.Time) bool { return l.State == StateActive && l.Expires.After(t) }

// blocking reports whether the record keeps others from the address at t.
func (l *Lease) blocking(t time.Time) bool {
	return (l.State == StateActive || l.State == StateDeclined) && l.Expires.After(t)
}

// Client identifies one identity association of a client.
type Client struct {
	DUID string
	IAID uint32
	Kind Kind
}

// Key is the map key of a client IA.
func (c Client) Key() string {
	return c.DUID + "/" + string(c.Kind) + "/" + strconv.FormatUint(uint64(c.IAID), 10)
}

// Key returns the client IA of a lease.
func (l *Lease) Key() string { return Client{l.DUID, l.IAID, l.Kind}.Key() }

// hash derives the k-th candidate index for a client IA (stable: the same
// client gets the same address back after its lease ended).
func hash(c Client, k int) uint64 {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", c.Key(), k)))
	return binary.BigEndian.Uint64(h[:8])
}

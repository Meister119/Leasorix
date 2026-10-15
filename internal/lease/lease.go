package lease

import (
	"time"

	"github.com/meister119/leasorix/internal/ip4"
)

// State is the persisted state of a lease record.
type State string

// Lease states.
const (
	// StateActive: address bound to the client until Expires.
	StateActive State = "active"
	// StateExpired: lease ran out; the record is kept for the grace period
	// so that the same client gets its address back.
	StateExpired State = "expired"
	// StateReleased: client released the address. The address is free again
	// but the record is kept for affinity until the grace period ends.
	StateReleased State = "released"
	// StateDeclined: address reported in use (DHCPDECLINE or failed conflict
	// probe). Not handed out until Expires.
	StateDeclined State = "declined"
)

// Infinite is the expiry used for unlimited leases (9999-12-31T23:59:59Z).
var Infinite = time.Unix(253402300799, 0).UTC()

// Lease is one lease record. The IP is the primary key.
type Lease struct {
	IP       uint32
	ScopeID  int64
	MAC      string
	ClientID string
	Hostname string
	State    State
	Start    time.Time
	Expires  time.Time
	Updated  time.Time
}

// Key returns the identity of the client owning the lease.
func (l *Lease) Key() ClientKey { return KeyOf(l.MAC, l.ClientID) }

// IPString returns the dotted-quad address.
func (l *Lease) IPString() string { return ip4.String(l.IP) }

// ActiveAt reports whether the lease binds the address at time t.
func (l *Lease) ActiveAt(t time.Time) bool { return l.State == StateActive && l.Expires.After(t) }

// ClientKey identifies a DHCP client: by client identifier (option 61) when
// present, by hardware address otherwise (RFC 2131 section 4.2).
type ClientKey string

// KeyOf builds the client key.
func KeyOf(mac, clientID string) ClientKey {
	if clientID != "" {
		return ClientKey("id:" + clientID)
	}
	return ClientKey("hw:" + mac)
}

// Client describes the requesting client.
type Client struct {
	MAC      string // canonical MAC
	ClientID string // hex option 61, may be empty
	Hostname string // option 12, may be empty
}

// Key returns the client key.
func (c Client) Key() ClientKey { return KeyOf(c.MAC, c.ClientID) }

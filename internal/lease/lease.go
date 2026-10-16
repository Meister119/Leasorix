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
	// Updated is the time of the last change (millisecond precision); it
	// orders changes of the same lease made on different HA servers.
	Updated time.Time
	// Origin is the HA node id of the server that made the last change
	// (empty without HA).
	Origin string
}

// Change is a lease change exchanged with an HA partner.
type Change struct {
	Lease Lease
	// Deleted: the record was removed by an administrator (only IP,
	// Updated and Origin are meaningful).
	Deleted bool
}

// binding reports whether the record blocks its address at time now.
func (l *Lease) binding(now time.Time) bool {
	return (l.State == StateActive || l.State == StateDeclined) && l.Expires.After(now)
}

func (l *Lease) newerThan(o *Lease) bool {
	if !l.Updated.Equal(o.Updated) {
		return l.Updated.After(o.Updated)
	}
	if !l.Expires.Equal(o.Expires) {
		return l.Expires.After(o.Expires)
	}
	return l.Origin > o.Origin
}

// Wins decides deterministically which of two records for the same address
// is kept when the lease tables of two HA servers are merged; it reports
// whether a replaces b. Both servers reach the same decision.
//
//   - Records of the same client: the newer change wins.
//   - Different clients, exactly one record binds the address: that one.
//   - Both bind the address (a real conflict, e.g. after a split brain):
//     the later lease end wins, then the higher node id.
//   - Neither binds the address: the newer change wins.
func Wins(a, b *Lease, now time.Time) bool {
	if a.State != StateDeclined && b.State != StateDeclined && a.Key() == b.Key() {
		return a.newerThan(b)
	}
	ab, bb := a.binding(now), b.binding(now)
	if ab != bb {
		return ab
	}
	if ab {
		if !a.Expires.Equal(b.Expires) {
			return a.Expires.After(b.Expires)
		}
		if a.Origin != b.Origin {
			return a.Origin > b.Origin
		}
	}
	return a.newerThan(b)
}

// Conflict reports whether a and b both bind the address for different
// clients (or one of them is a decline record).
func Conflict(a, b *Lease, now time.Time) bool {
	if !a.binding(now) || !b.binding(now) {
		return false
	}
	if a.State == StateDeclined || b.State == StateDeclined {
		return a.State != b.State
	}
	return a.Key() != b.Key()
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

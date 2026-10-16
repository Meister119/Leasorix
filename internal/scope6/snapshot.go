package scope6

import (
	"net/netip"
	"sort"

	"github.com/meister119/leasorix/internal/dhcpopt6"
)

// Range is an inclusive address range.
type Range struct{ Start, End netip.Addr }

// Contains reports whether a lies in the range.
func (r Range) Contains(a netip.Addr) bool { return r.Start.Compare(a) <= 0 && a.Compare(r.End) <= 0 }

// Pool is a scope with everything the engine needs.
type Pool struct {
	Scope
	Exclusions   []Range
	reservations []*Reservation
	resByIP      map[netip.Addr]*Reservation
	resByDUID    map[string][]*Reservation
	options      map[uint16][]byte
}

// InRange reports whether a is in the address range of the scope.
func (p *Pool) InRange(a netip.Addr) bool {
	return p.HasAddresses() && Range{p.Start, p.End}.Contains(a)
}

// Excluded reports whether a lies in an exclusion.
func (p *Pool) Excluded(a netip.Addr) bool {
	for _, e := range p.Exclusions {
		if e.Contains(a) {
			return true
		}
	}
	return false
}

// ReservationFor returns the reservation of a client IA: one for exactly
// this IAID first, then one for any IAID.
func (p *Pool) ReservationFor(duid string, iaid uint32) *Reservation {
	var any *Reservation
	for _, r := range p.resByDUID[duid] {
		if r.IAID == int64(iaid) {
			return r
		}
		if r.IAID == AnyIAID {
			any = r
		}
	}
	return any
}

// ReservedIP returns the reservation of an address, if any.
func (p *Pool) ReservedIP(a netip.Addr) *Reservation { return p.resByIP[a] }

// Snapshot is an immutable view of the DHCPv6 configuration.
type Snapshot struct {
	Version            uint64
	pools              []*Pool
	byID               map[int64]*Pool
	serverOptions      map[uint16][]byte
	reservationOptions map[int64]map[uint16][]byte
}

type snapshotData struct {
	scopes       []Scope
	exclusions   []Exclusion
	reservations []Reservation
	options      []OptionValue
}

func buildSnapshot(version uint64, d *snapshotData) *Snapshot {
	snap := &Snapshot{
		Version: version, byID: map[int64]*Pool{}, serverOptions: map[uint16][]byte{},
		reservationOptions: map[int64]map[uint16][]byte{},
	}
	for _, sc := range d.scopes {
		p := &Pool{Scope: sc, resByIP: map[netip.Addr]*Reservation{}, resByDUID: map[string][]*Reservation{}, options: map[uint16][]byte{}}
		snap.pools = append(snap.pools, p)
		snap.byID[sc.ID] = p
	}
	sort.Slice(snap.pools, func(i, j int) bool { return snap.pools[i].Prefix.Addr().Less(snap.pools[j].Prefix.Addr()) })
	for _, e := range d.exclusions {
		if p := snap.byID[e.ScopeID]; p != nil {
			p.Exclusions = append(p.Exclusions, Range{e.Start, e.End})
		}
	}
	for i := range d.reservations {
		r := &d.reservations[i]
		if p := snap.byID[r.ScopeID]; p != nil {
			p.reservations = append(p.reservations, r)
			p.resByIP[r.IP] = r
			p.resByDUID[r.DUID] = append(p.resByDUID[r.DUID], r)
		}
	}
	for _, ov := range d.options {
		_, wire, err := dhcpopt6.Parse(ov.Type, ov.Value)
		if err != nil {
			continue
		}
		switch ov.Owner.Kind {
		case OwnerServer:
			snap.serverOptions[ov.Code] = wire
		case OwnerScope:
			if p := snap.byID[ov.Owner.ID]; p != nil {
				p.options[ov.Code] = wire
			}
		case OwnerReservation:
			m := snap.reservationOptions[ov.Owner.ID]
			if m == nil {
				m = map[uint16][]byte{}
				snap.reservationOptions[ov.Owner.ID] = m
			}
			m[ov.Code] = wire
		}
	}
	return snap
}

// Pools returns all scopes ordered by prefix.
func (s *Snapshot) Pools() []*Pool { return s.pools }

// ByID returns a scope or nil.
func (s *Snapshot) ByID(id int64) *Pool { return s.byID[id] }

// ForAddr returns the scope whose link prefix contains a, or nil.
func (s *Snapshot) ForAddr(a netip.Addr) *Pool {
	for _, p := range s.pools {
		if p.Prefix.Contains(a) {
			return p
		}
	}
	return nil
}

// ForDelegated returns the scope whose delegation pool contains a, or
// nil.
func (s *Snapshot) ForDelegated(a netip.Addr) *Pool {
	for _, p := range s.pools {
		if p.HasPD() && p.PDPool.Contains(a) {
			return p
		}
	}
	return nil
}

// Options returns the option values for a client of scope p (server <
// scope < reservation).
func (s *Snapshot) Options(p *Pool, res *Reservation) map[uint16][]byte {
	out := make(map[uint16][]byte, len(s.serverOptions)+8)
	for k, v := range s.serverOptions {
		out[k] = v
	}
	if p != nil {
		for k, v := range p.options {
			out[k] = v
		}
	}
	if res != nil {
		for k, v := range s.reservationOptions[res.ID] {
			out[k] = v
		}
	}
	return out
}

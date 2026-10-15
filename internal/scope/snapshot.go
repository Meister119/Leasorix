package scope

import (
	"sort"

	"github.com/meister119/leasorix/internal/ip4"
)

// Pool is the read-optimised, immutable view of one scope used by the DHCP
// engine. All addresses are uint32.
type Pool struct {
	Scope
	Network    uint32
	Bits       int
	Mask       uint32
	Broadcast  uint32
	Range      ip4.Range
	Exclusions []ip4.Range // sorted by Start, non-overlapping

	reservations []*Reservation
	resByIP      map[uint32]*Reservation
	resByMAC     map[string]*Reservation
	resByCID     map[string]*Reservation
}

// InSubnet reports whether ip belongs to the scope's subnet (excluding the
// network and broadcast addresses).
func (p *Pool) InSubnet(ip uint32) bool {
	return ip4.Contains(p.Network, p.Bits, ip) && ip != p.Network && ip != p.Broadcast
}

// InRange reports whether ip lies in the dynamic range.
func (p *Pool) InRange(ip uint32) bool { return p.Range.Contains(ip) }

// Excluded reports whether ip is in an exclusion range.
func (p *Pool) Excluded(ip uint32) bool {
	i := sort.Search(len(p.Exclusions), func(i int) bool { return p.Exclusions[i].End >= ip })
	return i < len(p.Exclusions) && p.Exclusions[i].Start <= ip
}

// Dynamic reports whether ip may be handed out dynamically: inside the range,
// not excluded and not reserved.
func (p *Pool) Dynamic(ip uint32) bool {
	return p.InRange(ip) && !p.Excluded(ip) && p.resByIP[ip] == nil
}

// ReservationAt returns the reservation for ip, if any.
func (p *Pool) ReservationAt(ip uint32) *Reservation { return p.resByIP[ip] }

// ReservationFor returns the reservation matching the client. The client
// identifier takes precedence over the MAC address.
func (p *Pool) ReservationFor(mac, clientID string) *Reservation {
	if clientID != "" {
		if r := p.resByCID[clientID]; r != nil {
			return r
		}
	}
	if mac != "" {
		if r := p.resByMAC[mac]; r != nil {
			return r
		}
	}
	return nil
}

// Reservations returns all reservations of the scope ordered by IP.
func (p *Pool) Reservations() []*Reservation { return p.reservations }

// Size returns the number of addresses in the dynamic range.
func (p *Pool) Size() uint64 { return p.Range.Size() }

// Snapshot is an immutable view of all scopes.
type Snapshot struct {
	Version uint64
	pools   []*Pool // sorted by network
	byID    map[int64]*Pool
}

func emptySnapshot() *Snapshot { return &Snapshot{byID: map[int64]*Pool{}} }

// Pools returns all scopes ordered by network address.
func (s *Snapshot) Pools() []*Pool { return s.pools }

// ByID returns the scope with the given id or nil.
func (s *Snapshot) ByID(id int64) *Pool { return s.byID[id] }

// ForAddr returns the scope whose subnet contains ip, or nil. Subnets never
// overlap, so the result is unique.
func (s *Snapshot) ForAddr(ip uint32) *Pool {
	i := sort.Search(len(s.pools), func(i int) bool { return s.pools[i].Network > ip })
	if i == 0 {
		return nil
	}
	p := s.pools[i-1]
	if ip4.Contains(p.Network, p.Bits, ip) {
		return p
	}
	return nil
}

func buildSnapshot(version uint64, scopes []Scope, excl []Exclusion, res []Reservation) *Snapshot {
	snap := &Snapshot{Version: version, byID: make(map[int64]*Pool, len(scopes))}
	for _, sc := range scopes {
		network := ip4.MustFromAddr(sc.Subnet.Addr())
		bits := sc.Subnet.Bits()
		p := &Pool{
			Scope:     sc,
			Network:   network,
			Bits:      bits,
			Mask:      ip4.Mask(bits),
			Broadcast: ip4.Broadcast(network, bits),
			Range:     sc.Range(),
			resByIP:   map[uint32]*Reservation{},
			resByMAC:  map[string]*Reservation{},
			resByCID:  map[string]*Reservation{},
		}
		snap.pools = append(snap.pools, p)
		snap.byID[sc.ID] = p
	}
	sort.Slice(snap.pools, func(i, j int) bool { return snap.pools[i].Network < snap.pools[j].Network })
	for _, e := range excl {
		if p := snap.byID[e.ScopeID]; p != nil {
			p.Exclusions = append(p.Exclusions, ip4.Range{Start: ip4.MustFromAddr(e.Start), End: ip4.MustFromAddr(e.End)})
		}
	}
	for _, p := range snap.pools {
		sort.Slice(p.Exclusions, func(i, j int) bool { return p.Exclusions[i].Start < p.Exclusions[j].Start })
	}
	for i := range res {
		r := &res[i]
		p := snap.byID[r.ScopeID]
		if p == nil {
			continue
		}
		p.reservations = append(p.reservations, r)
		p.resByIP[ip4.MustFromAddr(r.IP)] = r
		if r.MAC != "" {
			p.resByMAC[r.MAC] = r
		}
		if r.ClientID != "" {
			p.resByCID[r.ClientID] = r
		}
	}
	return snap
}

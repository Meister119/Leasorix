package scope

import (
	"sort"
	"strings"

	"github.com/meister119/leasorix/internal/dhcpopt"
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

	options       map[uint8][]byte
	policies      []*PolicyView // scope level, processing order
	generalRanges []ip4.Range   // dynamic range minus policy ranges
}

// Policies returns the scope level policies in processing order.
func (p *Pool) Policies() []*PolicyView { return p.policies }

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

// Snapshot is an immutable view of the whole DHCP configuration.
type Snapshot struct {
	Version uint64
	pools   []*Pool // sorted by network
	byID    map[int64]*Pool
	// members of each superscope, sorted by network
	superscopes map[int64][]*Pool

	serverOptions      map[uint8][]byte
	reservationOptions map[int64]map[uint8][]byte
	serverPolicies     []*PolicyView
	classes            map[string]*Class // by lower-case name
	customDefs         map[uint8]dhcpopt.Definition
	filterSettings     FilterSettings
	allowList          []string
	denyList           []string
}

func emptySnapshot() *Snapshot {
	return &Snapshot{
		byID:               map[int64]*Pool{},
		superscopes:        map[int64][]*Pool{},
		serverOptions:      map[uint8][]byte{},
		reservationOptions: map[int64]map[uint8][]byte{},
		classes:            map[string]*Class{},
		customDefs:         map[uint8]dhcpopt.Definition{},
	}
}

// snapshotData is everything loaded from the database for a snapshot.
type snapshotData struct {
	scopes       []Scope
	exclusions   []Exclusion
	reservations []Reservation
	options      []OptionValue
	policies     []Policy
	classes      []Class
	customDefs   []dhcpopt.Definition
	filters      []MACFilter
	filterCfg    FilterSettings
}

// Pools returns all scopes ordered by network address.
func (s *Snapshot) Pools() []*Pool { return s.pools }

// ByID returns the scope with the given id or nil.
func (s *Snapshot) ByID(id int64) *Pool { return s.byID[id] }

// SuperscopePools returns the member scopes of a superscope, sorted by
// network.
func (s *Snapshot) SuperscopePools(id int64) []*Pool { return s.superscopes[id] }

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

func buildSnapshot(version uint64, d *snapshotData) *Snapshot {
	snap := emptySnapshot()
	snap.Version = version
	scopes, excl, res := d.scopes, d.exclusions, d.reservations
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
			options:   map[uint8][]byte{},
		}
		snap.pools = append(snap.pools, p)
		snap.byID[sc.ID] = p
	}
	sort.Slice(snap.pools, func(i, j int) bool { return snap.pools[i].Network < snap.pools[j].Network })
	for _, p := range snap.pools {
		if p.SuperscopeID != 0 {
			snap.superscopes[p.SuperscopeID] = append(snap.superscopes[p.SuperscopeID], p)
		}
	}
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

	for _, def := range d.customDefs {
		snap.customDefs[def.Code] = def
	}
	for i := range d.classes {
		c := &d.classes[i]
		snap.classes[strings.ToLower(c.Name)] = c
	}
	policyByID := map[int64]*PolicyView{}
	for _, pol := range d.policies {
		pv := &PolicyView{Policy: pol, options: map[uint8][]byte{}}
		for _, r := range pol.Ranges {
			pv.ranges = append(pv.ranges, r.ints())
		}
		policyByID[pol.ID] = pv
		if pol.ScopeID == 0 {
			snap.serverPolicies = append(snap.serverPolicies, pv)
		} else if p := snap.byID[pol.ScopeID]; p != nil {
			p.policies = append(p.policies, pv)
		}
	}
	byOrder := func(ps []*PolicyView) {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Order < ps[j].Order })
	}
	byOrder(snap.serverPolicies)
	for _, p := range snap.pools {
		byOrder(p.policies)
		var taken []ip4.Range
		for _, pv := range p.policies {
			if pv.Enabled {
				taken = append(taken, pv.ranges...)
			}
		}
		p.generalRanges = subtractRanges(p.Range, taken)
	}

	for _, ov := range d.options {
		_, wire, err := dhcpopt.Parse(ov.Type, ov.Value)
		if err != nil {
			continue // validated on write; skip corrupt rows
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
				m = map[uint8][]byte{}
				snap.reservationOptions[ov.Owner.ID] = m
			}
			m[ov.Code] = wire
		case OwnerPolicy:
			if pv := policyByID[ov.Owner.ID]; pv != nil {
				pv.options[ov.Code] = wire
			}
		}
	}

	snap.filterSettings = d.filterCfg
	for _, f := range d.filters {
		if f.List == FilterAllow {
			snap.allowList = append(snap.allowList, f.Pattern)
		} else {
			snap.denyList = append(snap.denyList, f.Pattern)
		}
	}
	return snap
}

// ExcludedCount returns the number of addresses of the dynamic range that
// are covered by exclusions.
func (p *Pool) ExcludedCount() uint64 {
	var n uint64
	for _, e := range p.Exclusions {
		start, end := max(e.Start, p.Range.Start), min(e.End, p.Range.End)
		if start <= end {
			n += uint64(end) - uint64(start) + 1
		}
	}
	return n
}

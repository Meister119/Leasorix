package lease

import (
	"time"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
)

// ScopeStats summarises the address usage of one scope.
type ScopeStats struct {
	ScopeID int64  `json:"scope_id"`
	Name    string `json:"name"`
	Subnet  string `json:"subnet"`
	// Total is the size of the dynamic range, Excluded the part of it
	// covered by exclusions, Usable = Total - Excluded.
	Total    uint64 `json:"total"`
	Excluded uint64 `json:"excluded"`
	Usable   uint64 `json:"usable"`
	// InUse counts active leases inside the usable range.
	InUse uint64 `json:"in_use"`
	// Reserved counts reservations inside the usable range that are not
	// in use right now.
	Reserved uint64 `json:"reserved"`
	// Declined counts blocked (declined / conflicting) addresses.
	Declined uint64 `json:"declined"`
	// Available = Usable - InUse - Reserved - Declined.
	Available uint64 `json:"available"`
	// Utilization is (Usable - Available) / Usable in percent.
	Utilization float64 `json:"utilization_percent"`
	// ActiveLeases counts all active leases of the scope, including
	// reservations outside the dynamic range.
	ActiveLeases uint64           `json:"active_leases"`
	ByState      map[State]uint64 `json:"by_state"`
}

// Stats computes the statistics of every scope in snap.
func (m *Manager) Stats(snap *scope.Snapshot) []ScopeStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]ScopeStats, 0, len(snap.Pools()))
	for _, p := range snap.Pools() {
		out = append(out, m.scopeStatsLocked(p, now))
	}
	return out
}

// ScopeStats computes the statistics of one scope.
func (m *Manager) ScopeStats(p *scope.Pool) ScopeStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scopeStatsLocked(p, m.now())
}

func (m *Manager) scopeStatsLocked(p *scope.Pool, now time.Time) ScopeStats {
	st := ScopeStats{
		ScopeID:  p.ID,
		Name:     p.Name,
		Subnet:   p.Subnet.String(),
		Total:    p.Size(),
		Excluded: p.ExcludedCount(),
		ByState:  map[State]uint64{StateActive: 0, StateExpired: 0, StateReleased: 0, StateDeclined: 0},
	}
	st.Usable = st.Total - st.Excluded
	usable := func(ip uint32) bool { return p.InRange(ip) && !p.Excluded(ip) }
	for ip, l := range m.byScope[p.ID] {
		state := effectiveState(l, now)
		if state == StateDeclined && !l.Expires.After(now) {
			continue // block ran out, purged on the next expiry run
		}
		st.ByState[state]++
		switch state {
		case StateActive:
			st.ActiveLeases++
			if usable(ip) {
				st.InUse++
			}
		case StateDeclined:
			if usable(ip) {
				st.Declined++
			}
		}
	}
	for _, r := range p.Reservations() {
		ip, ok := ip4.FromAddr(r.IP)
		if !ok || !usable(ip) {
			continue
		}
		if l := m.leases[ip]; l != nil && l.State != StateDeclined && effectiveState(l, now) == StateActive {
			continue // already counted as in use
		}
		st.Reserved++
	}
	used := st.InUse + st.Reserved + st.Declined
	if used > st.Usable {
		used = st.Usable
	}
	st.Available = st.Usable - used
	if st.Usable > 0 {
		st.Utilization = float64(used) / float64(st.Usable) * 100
	}
	return st
}

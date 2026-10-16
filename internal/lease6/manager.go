package lease6

import (
	"context"
	"database/sql"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/ip6"
	"github.com/meister119/leasorix/internal/scope6"
	"github.com/meister119/leasorix/internal/store"
)

// Config tunes the binding handling.
type Config struct {
	// OfferTimeout is how long an advertised address is held.
	OfferTimeout time.Duration
	// DeclineTime blocks a declined address.
	DeclineTime time.Duration
	// ExpiredGrace keeps expired and released records (affinity).
	ExpiredGrace time.Duration
	// ExpiryInterval is the period of the expiry worker.
	ExpiryInterval time.Duration
	// AuditEvents writes assign/release/decline/expire to the audit log.
	AuditEvents bool
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{OfferTimeout: time.Minute, DeclineTime: 24 * time.Hour, ExpiredGrace: 4 * time.Hour, ExpiryInterval: 10 * time.Second, AuditEvents: true}
}

// Coordinator restricts new allocations to this server's share when it
// runs in a high availability relationship.
type Coordinator interface {
	CanAllocate6(p netip.Prefix) bool
}

// EventCounter counts lease events (metrics); may be nil.
type EventCounter interface {
	LeaseEvent(event string)
}

type offer struct {
	prefix  netip.Prefix
	key     string
	scopeID int64
	expires time.Time
}

// Manager is the authoritative in-memory binding table.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	db     *store.DB
	now    func() time.Time
	coord  Coordinator
	events EventCounter

	mu       sync.Mutex
	leases   map[netip.Prefix]*Lease
	byClient map[string]map[netip.Prefix]*Lease
	offers   map[netip.Prefix]*offer
}

// NewManager creates a manager.
func NewManager(cfg Config, log *slog.Logger, db *store.DB) *Manager {
	return &Manager{
		cfg: cfg, log: log, db: db, now: time.Now,
		leases: map[netip.Prefix]*Lease{}, byClient: map[string]map[netip.Prefix]*Lease{}, offers: map[netip.Prefix]*offer{},
	}
}

// SetClock overrides the time source (tests).
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// SetCoordinator enables the high availability split.
func (m *Manager) SetCoordinator(c Coordinator) { m.coord = c }

// SetEvents enables event counting.
func (m *Manager) SetEvents(e EventCounter) { m.events = e }

func (m *Manager) event(name string) {
	if m.events != nil {
		m.events.LeaseEvent(name)
	}
}

func (m *Manager) put(l *Lease) {
	if old := m.leases[l.Prefix]; old != nil {
		m.unindex(old)
	}
	m.leases[l.Prefix] = l
	k := l.Key()
	if m.byClient[k] == nil {
		m.byClient[k] = map[netip.Prefix]*Lease{}
	}
	m.byClient[k][l.Prefix] = l
}

func (m *Manager) unindex(l *Lease) {
	k := l.Key()
	if set := m.byClient[k]; set != nil {
		delete(set, l.Prefix)
		if len(set) == 0 {
			delete(m.byClient, k)
		}
	}
}

func (m *Manager) remove(p netip.Prefix) {
	if l := m.leases[p]; l != nil {
		m.unindex(l)
		delete(m.leases, p)
	}
}

// ---- allocation ---------------------------------------------------------------------

// Request asks for a binding of one client IA in a scope.
type Request struct {
	Client   Client
	Pool     *scope6.Pool
	Hints    []netip.Prefix
	Hostname string
}

// free reports whether p may be given to client key in pool. strict also
// skips addresses with any record (prefers never used ones). allocate
// applies the high availability share.
func (m *Manager) free(r *Request, p netip.Prefix, strict bool, now time.Time) bool {
	key := r.Client.Key()
	pool := r.Pool
	if r.Client.Kind == KindNA {
		a := p.Addr()
		if p.Bits() != 128 || !pool.InRange(a) || pool.Excluded(a) {
			return false
		}
		if res := pool.ReservedIP(a); res != nil {
			return false // reserved addresses only via their reservation
		}
	} else if !pool.HasPD() || p.Bits() != pool.PDLength || p.Masked() != p || !pool.PDPool.Contains(p.Addr()) {
		return false
	}
	if rec := m.leases[p]; rec != nil && rec.Key() != key {
		if strict || rec.blocking(now) {
			return false
		}
	}
	if o := m.offers[p]; o != nil && o.key != key && o.expires.After(now) {
		return false
	}
	return m.coord == nil || m.coord.CanAllocate6(p)
}

// pick selects the binding for r (m.mu held).
func (m *Manager) pick(r *Request, now time.Time) (netip.Prefix, bool) {
	key := r.Client.Key()
	pool := r.Pool
	// A reservation wins (addresses only).
	if r.Client.Kind == KindNA {
		if res := pool.ReservationFor(r.Client.DUID, r.Client.IAID); res != nil {
			p := netip.PrefixFrom(res.IP, 128)
			if rec := m.leases[p]; rec == nil || rec.Key() == key || !rec.blocking(now) {
				return p, true
			}
		}
	}
	// The client's current binding or offer in this scope.
	for p, l := range m.byClient[key] {
		if l.ScopeID == pool.ID && l.ActiveAt(now) {
			return p, true
		}
	}
	for p, o := range m.offers {
		if o.key == key && o.scopeID == pool.ID && o.expires.After(now) && m.free(r, p, false, now) {
			return p, true
		}
	}
	for _, h := range r.Hints {
		if h.IsValid() && m.free(r, h, false, now) {
			return h, true
		}
	}
	// Its previous binding (affinity).
	for p, l := range m.byClient[key] {
		if l.ScopeID == pool.ID && l.State != StateDeclined && m.free(r, p, false, now) {
			return p, true
		}
	}
	return m.search(r, now)
}

// search finds a new address or prefix. Small ranges (up to 65536) are
// scanned from a client-specific start; larger ones are sampled with
// client-specific hashes, so the same client gets the same address again.
func (m *Manager) search(r *Request, now time.Time) (netip.Prefix, bool) {
	pool := r.Pool
	var (
		size uint64
		ok   bool
		at   func(i uint64) netip.Prefix
	)
	if r.Client.Kind == KindNA {
		if !pool.HasAddresses() {
			return netip.Prefix{}, false
		}
		size, ok = ip6.RangeSize(pool.Start, pool.End)
		at = func(i uint64) netip.Prefix { return netip.PrefixFrom(ip6.Offset(pool.Start, i), 128) }
	} else {
		if !pool.HasPD() {
			return netip.Prefix{}, false
		}
		size, ok = ip6.PrefixCount(pool.PDPool, pool.PDLength), true
		at = func(i uint64) netip.Prefix { return ip6.NthPrefix(pool.PDPool, pool.PDLength, i) }
	}
	const scanLimit = 1 << 16
	if ok && size <= scanLimit {
		first := hash(r.Client, 0) % size
		for _, strict := range []bool{true, false} {
			for i := range size {
				if p := at((first + i) % size); m.free(r, p, strict, now) {
					return p, true
				}
			}
		}
		return netip.Prefix{}, false
	}
	for k := range 64 {
		n := hash(r.Client, k)
		if ok {
			n %= size
		}
		if p := at(n); m.free(r, p, true, now) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// Lifetimes of a binding in pool.
func lifetimes(pool *scope6.Pool, now time.Time) (preferred, valid time.Time) {
	return now.Add(pool.PreferredLifetime), now.Add(pool.ValidLifetime)
}

// Assign selects a binding for the client IA. Without commit the result is
// only held for the client (ADVERTISE); with commit it is bound and
// persisted before Assign returns. ok is false if nothing is available.
func (m *Manager) Assign(ctx context.Context, r Request, commit bool) (Lease, bool, error) {
	m.mu.Lock()
	now := m.now().Truncate(time.Second)
	p, ok := m.pick(&r, now)
	if !ok {
		m.mu.Unlock()
		m.event("no_address")
		return Lease{}, false, nil
	}
	pref, valid := lifetimes(r.Pool, now)
	l := Lease{
		Prefix: p, Kind: r.Client.Kind, ScopeID: r.Pool.ID, DUID: r.Client.DUID, IAID: r.Client.IAID, Hostname: r.Hostname,
		State: StateActive, Start: now, Preferred: pref, Expires: valid, Updated: m.now(),
	}
	if !commit {
		m.offers[p] = &offer{prefix: p, key: r.Client.Key(), scopeID: r.Pool.ID, expires: now.Add(m.cfg.OfferTimeout)}
		m.mu.Unlock()
		return l, true, nil
	}
	renewal := false
	if old := m.leases[p]; old != nil && old.Key() == l.Key() && old.ActiveAt(now) {
		renewal, l.Start = true, old.Start
	}
	delete(m.offers, p)
	rec := l
	m.put(&rec)
	m.mu.Unlock()
	name := "assign"
	if renewal {
		name = "renew"
	}
	return l, true, m.persist(ctx, name, l)
}

// Extend renews the client's existing binding of p in pool (RENEW,
// REBIND). It returns false if the client has no such binding.
func (m *Manager) Extend(ctx context.Context, c Client, pool *scope6.Pool, p netip.Prefix, hostname string) (Lease, bool, error) {
	m.mu.Lock()
	now := m.now().Truncate(time.Second)
	rec := m.leases[p]
	if rec == nil || rec.Key() != c.Key() || rec.ScopeID != pool.ID || rec.State == StateDeclined || (rec.State != StateActive && !m.free(&Request{Client: c, Pool: pool}, p, false, now)) {
		m.mu.Unlock()
		return Lease{}, false, nil
	}
	rec.State = StateActive
	rec.Preferred, rec.Expires = lifetimes(pool, now)
	rec.Updated = m.now()
	if hostname != "" {
		rec.Hostname = hostname
	}
	l := *rec
	m.mu.Unlock()
	return l, true, m.persist(ctx, "renew", l)
}

// Release ends the client's binding of p.
func (m *Manager) Release(ctx context.Context, c Client, p netip.Prefix) (bool, error) {
	m.mu.Lock()
	now := m.now().Truncate(time.Second)
	rec := m.leases[p]
	if rec == nil || rec.Key() != c.Key() || !rec.ActiveAt(now) {
		m.mu.Unlock()
		return false, nil
	}
	rec.State, rec.Expires, rec.Updated = StateReleased, now, m.now()
	l := *rec
	m.mu.Unlock()
	return true, m.persist(ctx, "release", l)
}

// Decline blocks an address the client found in use (duplicate address
// detection).
func (m *Manager) Decline(ctx context.Context, c Client, p netip.Prefix) (bool, error) {
	m.mu.Lock()
	now := m.now().Truncate(time.Second)
	rec := m.leases[p]
	if rec == nil || rec.Key() != c.Key() {
		m.mu.Unlock()
		return false, nil
	}
	rec.State, rec.Expires, rec.Updated = StateDeclined, now.Add(m.cfg.DeclineTime), m.now()
	l := *rec
	m.mu.Unlock()
	return true, m.persist(ctx, "decline", l)
}

// ---- persistence --------------------------------------------------------------------

const leaseCols = `ip, prefix_len, kind, scope_id, duid, iaid, hostname, state, starts_at, preferred_until, expires_at, updated_at`

func (m *Manager) persist(ctx context.Context, event string, l Lease) error {
	m.event(event)
	return m.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := upsert(ctx, tx, l); err != nil {
			return err
		}
		if !m.cfg.AuditEvents || event == "renew" {
			return nil
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "lease6."+event, "lease6", l.Prefix.String(), details(l)))
	})
}

func upsert(ctx context.Context, tx *sql.Tx, l Lease) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO leases6 (`+leaseCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Prefix.Addr().String(), l.Prefix.Bits(), string(l.Kind), l.ScopeID, l.DUID, int64(l.IAID), l.Hostname, string(l.State),
		l.Start.Unix(), l.Preferred.Unix(), l.Expires.Unix(), l.Updated.UnixMilli())
	return err
}

func details(l Lease) map[string]any {
	d := map[string]any{"prefix": l.Prefix.String(), "kind": string(l.Kind), "scope_id": l.ScopeID, "duid": l.DUID, "iaid": l.IAID,
		"state": string(l.State), "expires": l.Expires.Format(time.RFC3339)}
	if l.Hostname != "" {
		d["hostname"] = l.Hostname
	}
	return d
}

// Load reads the persisted bindings.
func (m *Manager) Load(ctx context.Context) error {
	rows, err := m.db.R.QueryContext(ctx, `SELECT `+leaseCols+` FROM leases6`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leases, m.byClient, m.offers = map[netip.Prefix]*Lease{}, map[string]map[netip.Prefix]*Lease{}, map[netip.Prefix]*offer{}
	for rows.Next() {
		var (
			l                           Lease
			ip, kind, state             string
			bits                        int
			iaid, start, pref, exp, upd int64
		)
		if err := rows.Scan(&ip, &bits, &kind, &l.ScopeID, &l.DUID, &iaid, &l.Hostname, &state, &start, &pref, &exp, &upd); err != nil {
			return err
		}
		a, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		l.Prefix, l.Kind, l.State, l.IAID = netip.PrefixFrom(a, bits), Kind(kind), State(state), uint32(iaid)
		l.Start, l.Preferred, l.Expires, l.Updated = time.Unix(start, 0).UTC(), time.Unix(pref, 0).UTC(), time.Unix(exp, 0).UTC(), time.UnixMilli(upd).UTC()
		m.put(&l)
	}
	return rows.Err()
}

// Reload re-reads the table (after a restore).
func (m *Manager) Reload(ctx context.Context) error { return m.Load(ctx) }

// ---- expiry -----------------------------------------------------------------------------

// Run expires bindings until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	t := time.NewTicker(m.cfg.ExpiryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.Expire(ctx)
		}
	}
}

// Expire moves run-out bindings to expired and purges old records.
func (m *Manager) Expire(ctx context.Context) {
	m.mu.Lock()
	now := m.now()
	var changed []Lease
	var purged []netip.Prefix
	for p, l := range m.leases {
		switch {
		case l.State == StateActive && !l.Expires.After(now):
			l.State, l.Updated = StateExpired, now
			changed = append(changed, *l)
		case (l.State == StateExpired || l.State == StateReleased) && !l.Expires.Add(m.cfg.ExpiredGrace).After(now),
			l.State == StateDeclined && !l.Expires.After(now):
			m.remove(p)
			purged = append(purged, p)
		}
	}
	for p, o := range m.offers {
		if !o.expires.After(now) {
			delete(m.offers, p)
		}
	}
	m.mu.Unlock()
	if len(changed) == 0 && len(purged) == 0 {
		return
	}
	err := m.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, l := range changed {
			if err := upsert(ctx, tx, l); err != nil {
				return err
			}
			if m.cfg.AuditEvents {
				if err := audit.Insert(ctx, tx, audit.New(ctx, "lease6.expire", "lease6", l.Prefix.String(), details(l))); err != nil {
					return err
				}
			}
		}
		for _, p := range purged {
			if _, err := tx.ExecContext(ctx, `DELETE FROM leases6 WHERE ip = ?`, p.Addr().String()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		m.log.Error("persisting DHCPv6 lease expiry", "err", err)
	}
	for range changed {
		m.event("expire")
	}
}

// ScopesChanged assigns bindings to the scope containing them and drops
// the others (the database follows via ON DELETE CASCADE or
// store.RemapLeases).
func (m *Manager) ScopesChanged(snap *scope6.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, l := range m.leases {
		var pool *scope6.Pool
		if l.Kind == KindPD {
			pool = snap.ForDelegated(p.Addr())
		} else {
			pool = snap.ForAddr(p.Addr())
		}
		if pool == nil {
			m.remove(p)
			continue
		}
		l.ScopeID = pool.ID
	}
	for p, o := range m.offers {
		if snap.ByID(o.scopeID) == nil {
			delete(m.offers, p)
		}
	}
}

// ---- queries ------------------------------------------------------------------------------

// All returns copies of all bindings ordered by address.
func (m *Manager) All() []Lease {
	m.mu.Lock()
	out := make([]Lease, 0, len(m.leases))
	for _, l := range m.leases {
		out = append(out, *l)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix.Addr().Less(out[j].Prefix.Addr()) })
	return out
}

// Get returns the binding of p.
func (m *Manager) Get(p netip.Prefix) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.leases[p]
	if l == nil {
		return Lease{}, false
	}
	return *l, true
}

// ByClient returns the bindings of a client IA.
func (m *Manager) ByClient(c Client) []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Lease
	for _, l := range m.byClient[c.Key()] {
		out = append(out, *l)
	}
	return out
}

// Delete removes a binding (administrative action).
func (m *Manager) Delete(ctx context.Context, p netip.Prefix) (bool, error) {
	m.mu.Lock()
	l := m.leases[p]
	if l == nil {
		m.mu.Unlock()
		return false, nil
	}
	old := *l
	m.remove(p)
	delete(m.offers, p)
	m.mu.Unlock()
	m.event("delete")
	return true, m.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM leases6 WHERE ip = ?`, p.Addr().String()); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "lease6.delete", "lease6", p.String(), details(old)))
	})
}

// ScopeStats are the binding counts of a scope.
type ScopeStats struct {
	ScopeID  int64
	Active   int // addresses
	Prefixes int // delegated prefixes
	Declined int
	// Size is the number of addresses in the range (capped at 2^64-1),
	// PrefixCapacity the number of delegable prefixes.
	Size           uint64
	PrefixCapacity uint64
}

// Stats returns per scope counts.
func (m *Manager) Stats(snap *scope6.Snapshot) []ScopeStats {
	now := m.now()
	byID := map[int64]*ScopeStats{}
	var out []ScopeStats
	for _, p := range snap.Pools() {
		st := ScopeStats{ScopeID: p.ID}
		if p.HasAddresses() {
			if n, ok := ip6.RangeSize(p.Start, p.End); ok {
				st.Size = n
			} else {
				st.Size = ^uint64(0)
			}
		}
		if p.HasPD() {
			st.PrefixCapacity = ip6.PrefixCount(p.PDPool, p.PDLength)
		}
		out = append(out, st)
	}
	for i := range out {
		byID[out[i].ScopeID] = &out[i]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.leases {
		st := byID[l.ScopeID]
		if st == nil {
			continue
		}
		switch {
		case l.State == StateDeclined && l.Expires.After(now):
			st.Declined++
		case l.ActiveAt(now) && l.Kind == KindPD:
			st.Prefixes++
		case l.ActiveAt(now):
			st.Active++
		}
	}
	return out
}

package lease

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
)

// Config tunes lease handling.
type Config struct {
	// OfferTimeout is how long an offered address stays reserved for the
	// client waiting for its DHCPREQUEST.
	OfferTimeout time.Duration
	// DeclineTime is how long a declined / conflicting address is blocked.
	DeclineTime time.Duration
	// ExpiredGrace is how long expired and released records are kept so the
	// same client gets its previous address back.
	ExpiredGrace time.Duration
	// ExpiryInterval is the period of the expiry worker.
	ExpiryInterval time.Duration
	// AuditEvents writes lease events (assign, release, decline, conflict,
	// expire) to the audit log.
	AuditEvents bool
	// AuditRenewals additionally audits every renewal (high volume).
	AuditRenewals bool
	// NodeID is recorded as origin of local changes (HA node id).
	NodeID string
}

// DefaultConfig returns the default lease settings.
func DefaultConfig() Config {
	return Config{
		OfferTimeout:   60 * time.Second,
		DeclineTime:    24 * time.Hour,
		ExpiredGrace:   4 * time.Hour,
		ExpiryInterval: 10 * time.Second,
		AuditEvents:    true,
	}
}

// Prober performs conflict detection before an address is offered.
type Prober interface {
	// InUse reports whether ip already answers on the network. clientMAC is
	// the requesting client; a reply from that very client is no conflict.
	InUse(ctx context.Context, ip uint32, clientMAC string) bool
}

// Coordinator connects the manager to a high availability partner.
type Coordinator interface {
	// CanAllocate reports whether this server may bind ip to a client that
	// does not hold an active lease on it (pool share of this server).
	CanAllocate(ip uint32) bool
	// LeaseLimit is the longest lease that may be granted right now
	// without confirmation by the partner (0 = no limit).
	LeaseLimit() time.Duration
	// Replicate forwards lease changes to the partner. For critical
	// changes (new or renewed bindings) in synchronous mode it waits for the
	// partner and reports whether it confirmed; otherwise it queues the
	// changes and returns false.
	Replicate(ctx context.Context, changes []Change, critical bool) bool
}

func limitLease(d, limit time.Duration) time.Duration {
	if limit > 0 && (d <= 0 || d > limit) {
		return limit
	}
	return d
}

type cursorKey struct {
	scopeID int64
	start   uint32
}

// Candidate is a scope the client may get an address from, together with
// the policy constraints that apply to this client there.
type Candidate struct {
	Pool *scope.Pool
	// Ranges restricts dynamic allocation to these ranges, in preference
	// order. nil means the whole dynamic range of the pool; an empty
	// non-nil slice allows no dynamic address at all.
	Ranges []ip4.Range
	// LeaseTime is the lease duration to grant (0 = unlimited).
	LeaseTime time.Duration
}

// PoolCandidate returns an unrestricted candidate using the scope lease time.
func PoolCandidate(p *scope.Pool) Candidate {
	return Candidate{Pool: p, LeaseTime: p.LeaseTime}
}

func (c *Candidate) allowed(ip uint32) bool {
	if c.Ranges == nil {
		return true
	}
	for _, r := range c.Ranges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *Candidate) ranges() []ip4.Range {
	if c.Ranges == nil {
		return []ip4.Range{c.Pool.Range}
	}
	return c.Ranges
}

type offerKey struct {
	key     ClientKey
	scopeID int64
}

type offer struct {
	ip      uint32
	scopeID int64
	key     ClientKey
	expires time.Time
	probed  bool
}

// Manager is the authoritative in-memory lease table. All decisions are made
// under a single mutex; persistence happens outside the lock through the
// group-commit writer. Memory may run ahead of disk only in the conservative
// direction (an address is considered used before its record is durable).
type Manager struct {
	cfg    Config
	log    *slog.Logger
	db     *DBStore
	prober Prober
	now    func() time.Time

	mu       sync.Mutex
	leases   map[uint32]*Lease
	byClient map[ClientKey]map[uint32]*Lease
	byScope  map[int64]map[uint32]*Lease
	offers   map[uint32]*offer
	offerBy  map[offerKey]*offer
	cursor   map[cursorKey]uint32

	clientLocks keyedMutex
	metrics     *metrics.Leases
	coord       Coordinator
}

// SetCoordinator enables HA coordination. It must be called before the
// manager serves requests.
func (m *Manager) SetCoordinator(c Coordinator) { m.coord = c }

func (m *Manager) canAllocate(ip uint32) bool { return m.coord == nil || m.coord.CanAllocate(ip) }

func (m *Manager) leaseLimit() time.Duration {
	if m.coord == nil {
		return 0
	}
	return m.coord.LeaseLimit()
}

func (m *Manager) replicate(ctx context.Context, critical bool, changes ...Change) bool {
	if m.coord == nil || len(changes) == 0 {
		return true
	}
	return m.coord.Replicate(ctx, changes, critical)
}

// stamps returns the second-precision time used for lease start/expiry and
// the millisecond-precision change time.
func (m *Manager) stamps() (now, updated time.Time) {
	t := m.now()
	return t.Truncate(time.Second), t.Truncate(time.Millisecond)
}

// SetMetrics enables lease metrics (events and commit timings).
func (m *Manager) SetMetrics(lm *metrics.Leases) {
	m.metrics = lm
	m.db.SetMetrics(lm)
}

func leaseDetails(l *Lease) map[string]any {
	d := map[string]any{
		"ip": ip4.String(l.IP), "scope_id": l.ScopeID, "state": string(l.State),
		"mac": l.MAC, "expires": l.Expires.Format(time.RFC3339),
	}
	if l.ClientID != "" {
		d["client_id"] = l.ClientID
	}
	if l.Hostname != "" {
		d["hostname"] = l.Hostname
	}
	return d
}

// event counts a lease event and appends its audit entry to ops.
func (m *Manager) event(ctx context.Context, ops []Op, name string, l *Lease, extra map[string]any) []Op {
	m.metrics.Event(name)
	if !m.cfg.AuditEvents || (name == "renew" && !m.cfg.AuditRenewals) {
		return ops
	}
	d := leaseDetails(l)
	for k, v := range extra {
		d[k] = v
	}
	return append(ops, AuditOp(audit.New(ctx, "lease."+name, "lease", ip4.String(l.IP), d)))
}

// NewManager creates a manager. prober may be nil (conflict detection off).
func NewManager(cfg Config, log *slog.Logger, db *DBStore, prober Prober) *Manager {
	return &Manager{
		cfg:      cfg,
		log:      log,
		db:       db,
		prober:   prober,
		now:      time.Now,
		leases:   map[uint32]*Lease{},
		byClient: map[ClientKey]map[uint32]*Lease{},
		byScope:  map[int64]map[uint32]*Lease{},
		offers:   map[uint32]*offer{},
		offerBy:  map[offerKey]*offer{},
		cursor:   map[cursorKey]uint32{},
	}
}

// SetClock overrides the time source (tests).
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// Load reads all leases from the database into memory.
func (m *Manager) Load(ctx context.Context) error {
	all, err := m.db.LoadAll(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range all {
		l := all[i]
		m.putLocked(&l)
	}
	m.log.Info("leases loaded", "count", len(all))
	return nil
}

// Run starts the persistence writer and the expiry worker.
func (m *Manager) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return m.db.Run(gctx) })
	g.Go(func() error {
		t := time.NewTicker(m.cfg.ExpiryInterval)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
				m.Expire(gctx)
			}
		}
	})
	return g.Wait()
}

// LockClient serialises processing for one client. It returns the unlock
// function.
func (m *Manager) LockClient(c Client) func() { return m.clientLocks.Lock(string(c.Key())) }

// ---- index maintenance (caller holds m.mu) ---------------------------------

func (m *Manager) putLocked(l *Lease) {
	if old := m.leases[l.IP]; old != nil {
		m.unindexLocked(old)
	}
	m.leases[l.IP] = l
	if m.byScope[l.ScopeID] == nil {
		m.byScope[l.ScopeID] = map[uint32]*Lease{}
	}
	m.byScope[l.ScopeID][l.IP] = l
	if l.State != StateDeclined {
		k := l.Key()
		if m.byClient[k] == nil {
			m.byClient[k] = map[uint32]*Lease{}
		}
		m.byClient[k][l.IP] = l
	}
}

func (m *Manager) unindexLocked(l *Lease) {
	if s := m.byScope[l.ScopeID]; s != nil {
		delete(s, l.IP)
		if len(s) == 0 {
			delete(m.byScope, l.ScopeID)
		}
	}
	k := l.Key()
	if c := m.byClient[k]; c != nil {
		delete(c, l.IP)
		if len(c) == 0 {
			delete(m.byClient, k)
		}
	}
}

func (m *Manager) removeLocked(ip uint32) {
	if l := m.leases[ip]; l != nil {
		m.unindexLocked(l)
		delete(m.leases, ip)
	}
}

func (m *Manager) setOfferLocked(key ClientKey, scopeID int64, ip uint32, probed bool, now time.Time) {
	ok := offerKey{key, scopeID}
	if prev := m.offerBy[ok]; prev != nil && prev.ip != ip {
		delete(m.offers, prev.ip)
	}
	o := &offer{ip: ip, scopeID: scopeID, key: key, expires: now.Add(m.cfg.OfferTimeout), probed: probed}
	m.offers[ip] = o
	m.offerBy[ok] = o
}

func (m *Manager) dropOfferLocked(key ClientKey, scopeID int64) {
	ok := offerKey{key, scopeID}
	if o := m.offerBy[ok]; o != nil {
		delete(m.offerBy, ok)
		if cur := m.offers[o.ip]; cur == o {
			delete(m.offers, o.ip)
		}
	}
}

// effectiveState accounts for leases that ran out but were not yet
// processed by the expiry worker.
func effectiveState(l *Lease, now time.Time) State {
	if l.State == StateActive && !l.Expires.After(now) {
		return StateExpired
	}
	return l.State
}

func (m *Manager) validOfferLocked(ip uint32, now time.Time) *offer {
	if o := m.offers[ip]; o != nil && o.expires.After(now) {
		return o
	}
	return nil
}

// checkAddressLocked decides whether ip in the candidate's pool may be bound
// to client c. offered=true means we offered ip to c ourselves, which lifts
// the affinity protection of expired records of other clients.
func (m *Manager) checkAddressLocked(cand *Candidate, c Client, ip uint32, offered bool, now time.Time) (bool, string) {
	p := cand.Pool
	if !p.Enabled {
		return false, "scope is disabled"
	}
	if !p.InSubnet(ip) {
		return false, "address not in scope subnet"
	}
	key := c.Key()
	res := p.ReservationFor(c.MAC, c.ClientID)
	if res != nil && ip4.MustFromAddr(res.IP) != ip {
		return false, "client has a reservation for " + res.IP.String()
	}
	if r := p.ReservationAt(ip); r != nil && r != res {
		return false, "address is reserved for another client"
	}
	if res == nil && !p.Dynamic(ip) {
		return false, "address is outside the dynamic range or excluded"
	}
	if res == nil && !cand.allowed(ip) {
		return false, "address is outside the ranges allowed for this client by policy"
	}
	if l := m.leases[ip]; l != nil && (l.State == StateDeclined || l.Key() != key) {
		switch effectiveState(l, now) {
		case StateActive:
			return false, "address is leased to another client"
		case StateDeclined:
			if l.Expires.After(now) {
				return false, "address is declined / in conflict"
			}
		case StateExpired:
			if !offered && l.Expires.Add(m.cfg.ExpiredGrace).After(now) {
				return false, "address is held for its previous client"
			}
		case StateReleased:
		}
	}
	if o := m.validOfferLocked(ip, now); o != nil && o.key != key {
		return false, "address is offered to another client"
	}
	if res == nil && !m.canAllocate(ip) && !m.ownsActiveLocked(key, ip, now) {
		return false, "address belongs to the HA partner's share of the pool"
	}
	return true, ""
}

func (m *Manager) ownsActiveLocked(key ClientKey, ip uint32, now time.Time) bool {
	l := m.leases[ip]
	return l != nil && l.State != StateDeclined && l.Key() == key && effectiveState(l, now) == StateActive
}

// ---- DISCOVER ----------------------------------------------------------------

// Offer is the result of a successful address selection.
type Offer struct {
	IP          uint32
	Pool        *scope.Pool
	Reservation *scope.Reservation
	// LeaseTime is the lease duration to announce (0 = unlimited).
	LeaseTime time.Duration
}

// Offer selects an address for a DHCPDISCOVER. cands are the scopes of the
// client's link in preference order; requested is option 50 (0 if absent).
// It returns nil if no address is available.
//
// Selection order: reservation > outstanding offer > existing lease of the
// client > requested address > never used address > released address >
// oldest expired address. New addresses are probed for conflicts first.
func (m *Manager) Offer(ctx context.Context, c Client, cands []Candidate, requested uint32) *Offer {
	key := c.Key()
	skip := map[uint32]bool{}
	for range 8 {
		m.mu.Lock()
		now := m.now()
		cand, ip, res, probed := m.chooseLocked(c, cands, requested, skip, now)
		if cand == nil {
			m.mu.Unlock()
			return nil
		}
		p := cand.Pool
		needProbe := !probed && !m.ownsActiveLocked(key, ip, now)
		m.setOfferLocked(key, p.ID, ip, true, now)
		m.mu.Unlock()

		if needProbe && m.prober != nil && m.prober.InUse(ctx, ip, c.MAC) {
			m.log.Warn("address conflict detected, address blocked", "ip", ip4.String(ip), "scope", p.Name, "client", c.MAC)
			skip[ip] = true
			m.mu.Lock()
			m.dropOfferLocked(key, p.ID)
			var (
				ops     []Op
				changes []Change
			)
			if res == nil {
				now, upd := m.stamps()
				l := &Lease{IP: ip, ScopeID: p.ID, State: StateDeclined, Start: now, Expires: now.Add(m.cfg.DeclineTime), Updated: upd, Origin: m.cfg.NodeID}
				m.putLocked(l)
				ops = append(ops, Upsert(*l))
				ops = m.event(ctx, ops, "conflict", l, map[string]any{"requested_by": c.MAC})
				changes = append(changes, Change{Lease: *l})
			}
			m.mu.Unlock()
			if len(ops) > 0 {
				if err := m.db.Apply(ctx, ops...); err != nil {
					m.log.Error("persisting conflict record", "ip", ip4.String(ip), "err", err)
				} else {
					m.replicate(ctx, false, changes...)
				}
			}
			if res != nil {
				m.log.Warn("reserved address is in use by another device; client gets no offer", "ip", ip4.String(ip), "client", c.MAC)
				return nil
			}
			continue
		}
		return &Offer{IP: ip, Pool: p, Reservation: res, LeaseTime: limitLease(cand.LeaseTime, m.leaseLimit())}
	}
	return nil
}

// chooseLocked returns the candidate and address to offer. probed reports
// whether the address was already probed for this client (outstanding offer).
func (m *Manager) chooseLocked(c Client, cands []Candidate, requested uint32, skip map[uint32]bool, now time.Time) (*Candidate, uint32, *scope.Reservation, bool) {
	key := c.Key()
	usable := func(cand *Candidate, ip uint32) bool {
		if skip[ip] {
			return false
		}
		ok, _ := m.checkAddressLocked(cand, c, ip, false, now)
		return ok
	}

	// 1. Reservation.
	for i := range cands {
		cand := &cands[i]
		p := cand.Pool
		if res := p.ReservationFor(c.MAC, c.ClientID); res != nil {
			ip := ip4.MustFromAddr(res.IP)
			if !usable(cand, ip) {
				m.log.Warn("reserved address currently unavailable", "ip", res.IP, "client", c.MAC)
				return nil, 0, nil, false
			}
			o := m.offerBy[offerKey{key, p.ID}]
			return cand, ip, res, o != nil && o.ip == ip && o.probed && o.expires.After(now)
		}
	}
	// 2. Outstanding offer.
	for i := range cands {
		cand := &cands[i]
		if o := m.offerBy[offerKey{key, cand.Pool.ID}]; o != nil && o.expires.After(now) && usable(cand, o.ip) {
			return cand, o.ip, nil, o.probed
		}
	}
	// 3. Existing lease of the client: active first, then most recently updated.
	var (
		bestCand  *Candidate
		bestLease *Lease
	)
	for i := range cands {
		cand := &cands[i]
		for ip, l := range m.byClient[key] {
			if l.ScopeID != cand.Pool.ID || !usable(cand, ip) {
				continue
			}
			if bestLease == nil || betterAffinity(l, bestLease, now) {
				bestCand, bestLease = cand, l
			}
		}
	}
	if bestLease != nil {
		return bestCand, bestLease.IP, nil, false
	}
	// 4. Requested address.
	if requested != 0 {
		for i := range cands {
			cand := &cands[i]
			if cand.Pool.InSubnet(requested) && usable(cand, requested) {
				return cand, requested, nil, false
			}
		}
	}
	// 5. Free address.
	for i := range cands {
		cand := &cands[i]
		if !cand.Pool.Enabled {
			continue
		}
		if ip, ok := m.findFreeLocked(cand, key, skip, now); ok {
			return cand, ip, nil, false
		}
	}
	return nil, 0, nil, false
}

func betterAffinity(a, b *Lease, now time.Time) bool {
	aa, ba := effectiveState(a, now) == StateActive, effectiveState(b, now) == StateActive
	if aa != ba {
		return aa
	}
	return a.Updated.After(b.Updated)
}

func (m *Manager) findFreeLocked(cand *Candidate, key ClientKey, skip map[uint32]bool, now time.Time) (uint32, bool) {
	p := cand.Pool
	for _, r := range cand.ranges() {
		ck := cursorKey{p.ID, r.Start}
		ip := m.cursor[ck]
		if !r.Contains(ip) {
			ip = r.Start
		}
		size := r.Size()
		for i := uint64(0); i < size; i++ {
			if !skip[ip] && m.leases[ip] == nil && p.Dynamic(ip) && m.canAllocate(ip) {
				if o := m.validOfferLocked(ip, now); o == nil || o.key == key {
					next := ip + 1
					if next > r.End || next == 0 {
						next = r.Start
					}
					m.cursor[ck] = next
					return ip, true
				}
			}
			if ip == r.End {
				ip = r.Start
			} else {
				ip++
			}
		}
	}
	// No untouched address left: reuse released, then expired records of
	// other clients (oldest first).
	var released, expired *Lease
	for ip, l := range m.byScope[p.ID] {
		if skip[ip] || !p.Dynamic(ip) || !cand.allowed(ip) || l.State == StateDeclined || l.Key() == key || !m.canAllocate(ip) {
			continue
		}
		if o := m.validOfferLocked(ip, now); o != nil && o.key != key {
			continue
		}
		switch effectiveState(l, now) {
		case StateReleased:
			if released == nil || l.Updated.Before(released.Updated) {
				released = l
			}
		case StateExpired:
			if expired == nil || l.Expires.Before(expired.Expires) {
				expired = l
			}
		}
	}
	if released != nil {
		return released.IP, true
	}
	if expired != nil {
		return expired.IP, true
	}
	return 0, false
}

// CancelOffer drops the client's outstanding offers (client chose another
// server).
func (m *Manager) CancelOffer(c Client, pools []*scope.Pool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pools {
		m.dropOfferLocked(c.Key(), p.ID)
	}
}

// ---- REQUEST -------------------------------------------------------------------

// Result of a DHCPREQUEST.
type Result struct {
	Ack   bool
	Lease Lease
	// LeaseTime is the granted lease duration (0 = unlimited).
	LeaseTime time.Duration
	Reason    string // NAK reason
}

// Request binds ip in the candidate's pool to the client if allowed and
// persists the lease before returning. An error means the lease could not be
// stored; the caller must not acknowledge.
//
// With an HA partner the binding is replicated before it is stored; if the
// partner cannot confirm it (synchronous mode), the lease is shortened to the
// limit allowed without the partner's knowledge (MCLT).
func (m *Manager) Request(ctx context.Context, c Client, cand Candidate, ip uint32) (Result, error) {
	key := c.Key()
	p := cand.Pool
	leaseTime := limitLease(cand.LeaseTime, m.leaseLimit())
	m.mu.Lock()
	now, upd := m.stamps()
	o := m.validOfferLocked(ip, now)
	offered := o != nil && o.key == key
	if ok, reason := m.checkAddressLocked(&cand, c, ip, offered, now); !ok {
		m.mu.Unlock()
		m.metrics.Event("nak")
		return Result{Reason: reason}, nil
	}
	prev := m.leases[ip]
	renewal := prev != nil && prev.State != StateDeclined && prev.Key() == key && effectiveState(prev, now) == StateActive
	res := p.ReservationFor(c.MAC, c.ClientID)
	hostname := c.Hostname
	if hostname == "" && res != nil {
		hostname = res.Hostname
	}
	expires := Infinite
	if leaseTime > 0 {
		expires = now.Add(leaseTime)
	}
	l := &Lease{
		IP: ip, ScopeID: p.ID, MAC: c.MAC, ClientID: c.ClientID, Hostname: hostname,
		State: StateActive, Start: now, Expires: expires, Updated: upd, Origin: m.cfg.NodeID,
	}
	// One binding per client and scope: release older ones.
	var superseded []Lease
	for oip, ol := range m.byClient[key] {
		if oip != ip && ol.ScopeID == p.ID && ol.State == StateActive {
			ol.State, ol.Expires, ol.Updated, ol.Origin = StateReleased, now, upd, m.cfg.NodeID
			superseded = append(superseded, *ol)
		}
	}
	m.putLocked(l)
	m.dropOfferLocked(key, p.ID)
	out := *l
	m.mu.Unlock()

	if m.coord != nil {
		changes := make([]Change, 0, 1+len(superseded))
		changes = append(changes, Change{Lease: out})
		for _, sl := range superseded {
			changes = append(changes, Change{Lease: sl})
		}
		if !m.coord.Replicate(ctx, changes, true) {
			if short := limitLease(leaseTime, m.coord.LeaseLimit()); short != leaseTime {
				leaseTime = short
				m.mu.Lock()
				out.Expires = now.Add(short)
				if m.leases[ip] == l {
					l.Expires = out.Expires
				}
				m.mu.Unlock()
			}
		}
	}

	ops := []Op{Upsert(out)}
	if renewal {
		ops = m.event(ctx, ops, "renew", &out, nil)
	} else {
		ops = m.event(ctx, ops, "assign", &out, nil)
	}
	for i := range superseded {
		ops = append(ops, Upsert(superseded[i]))
		ops = m.event(ctx, ops, "release", &superseded[i], map[string]any{"reason": "superseded by " + ip4.String(ip)})
	}
	if err := m.db.Apply(ctx, ops...); err != nil {
		return Result{}, err
	}
	return Result{Ack: true, Lease: out, LeaseTime: leaseTime}, nil
}

// ---- RELEASE / DECLINE ---------------------------------------------------------

// Release marks the client's lease on ip as released. It returns false if the
// client does not hold that lease.
func (m *Manager) Release(ctx context.Context, c Client, ip uint32) (bool, error) {
	m.mu.Lock()
	now, upd := m.stamps()
	l := m.leases[ip]
	if l == nil || l.State == StateDeclined || l.Key() != c.Key() || effectiveState(l, now) != StateActive {
		m.mu.Unlock()
		return false, nil
	}
	l.State, l.Expires, l.Updated, l.Origin = StateReleased, now, upd, m.cfg.NodeID
	out := *l
	ops := m.event(ctx, []Op{Upsert(out)}, "release", &out, nil)
	m.mu.Unlock()
	if err := m.db.Apply(ctx, ops...); err != nil {
		return true, err
	}
	m.replicate(ctx, false, Change{Lease: out})
	return true, nil
}

// Decline blocks ip after the client reported it in use. It returns false if
// the address was neither leased nor offered to the client.
func (m *Manager) Decline(ctx context.Context, c Client, p *scope.Pool, ip uint32) (bool, error) {
	key := c.Key()
	m.mu.Lock()
	now, upd := m.stamps()
	l := m.leases[ip]
	owned := l != nil && l.State != StateDeclined && l.Key() == key
	o := m.validOfferLocked(ip, now)
	if !owned && (o == nil || o.key != key) {
		m.mu.Unlock()
		return false, nil
	}
	m.dropOfferLocked(key, p.ID)
	d := &Lease{IP: ip, ScopeID: p.ID, MAC: c.MAC, State: StateDeclined, Start: now, Expires: now.Add(m.cfg.DeclineTime), Updated: upd, Origin: m.cfg.NodeID}
	m.putLocked(d)
	out := *d
	ops := m.event(ctx, []Op{Upsert(out)}, "decline", &out, nil)
	m.mu.Unlock()
	if err := m.db.Apply(ctx, ops...); err != nil {
		return true, err
	}
	m.replicate(ctx, false, Change{Lease: out})
	return true, nil
}

// ---- HA ------------------------------------------------------------------------

// ApplyRemote merges lease changes received from the HA partner (see Wins).
// The scope of a record is determined from its address, records outside all
// local scopes are skipped. Changes are persisted but neither audited
// individually nor replicated back. It returns the number of records that
// changed the local table and the number of conflicts that were resolved.
func (m *Manager) ApplyRemote(ctx context.Context, snap *scope.Snapshot, changes []Change) (applied, conflicts int, err error) {
	changed, conflicts, err := m.merge(ctx, snap, changes)
	return len(changed), conflicts, err
}

// Import merges records from a lease export with the same rules and
// forwards the records that changed the local table to the HA partner.
func (m *Manager) Import(ctx context.Context, snap *scope.Snapshot, leases []Lease) (applied, conflicts int, err error) {
	changes := make([]Change, len(leases))
	for i := range leases {
		changes[i] = Change{Lease: leases[i]}
	}
	changed, conflicts, err := m.merge(ctx, snap, changes)
	if err != nil {
		return len(changed), conflicts, err
	}
	if len(changed) > 0 {
		m.replicate(ctx, false, changed...)
	}
	return len(changed), conflicts, nil
}

func (m *Manager) merge(ctx context.Context, snap *scope.Snapshot, changes []Change) (changed []Change, conflicts int, err error) {
	m.mu.Lock()
	now := m.now()
	var ops []Op
	for i := range changes {
		ch := changes[i]
		r := ch.Lease
		local := m.leases[r.IP]
		if ch.Deleted {
			if local != nil && !local.Updated.After(r.Updated) {
				m.removeLocked(r.IP)
				ops = append(ops, Delete(r.IP))
				changed = append(changed, ch)
			}
			continue
		}
		p := snap.ForAddr(r.IP)
		if p == nil {
			continue
		}
		r.ScopeID = p.ID
		if local != nil {
			conflict := Conflict(&r, local, now)
			wins := Wins(&r, local, now)
			if conflict {
				conflicts++
				kept, lost := local, &r
				if wins {
					kept, lost = &r, local
				}
				m.log.Warn("HA lease conflict resolved", "ip", ip4.String(r.IP),
					"kept_mac", kept.MAC, "kept_origin", kept.Origin, "dropped_mac", lost.MAC, "dropped_origin", lost.Origin)
				if m.cfg.AuditEvents {
					d := leaseDetails(kept)
					d["dropped"] = leaseDetails(lost)
					ops = append(ops, AuditOp(audit.New(ctx, "lease.ha_conflict", "lease", ip4.String(r.IP), d)))
				}
			}
			if !wins {
				continue
			}
		}
		rec := r
		m.putLocked(&rec)
		ops = append(ops, Upsert(rec))
		changed = append(changed, Change{Lease: rec})
	}
	m.mu.Unlock()
	if len(ops) == 0 {
		return changed, conflicts, nil
	}
	return changed, conflicts, m.db.Apply(ctx, ops...)
}

// ---- expiry --------------------------------------------------------------------

// Expire moves run-out leases to expired, deletes records past the grace
// period / decline time and drops stale offers.
func (m *Manager) Expire(ctx context.Context) {
	m.mu.Lock()
	now, upd := m.stamps()
	var ops []Op
	var expired, purged int
	for ip, l := range m.leases {
		switch l.State {
		case StateActive:
			if !l.Expires.After(now) {
				l.State, l.Updated, l.Origin = StateExpired, upd, m.cfg.NodeID
				ops = append(ops, Upsert(*l))
				ops = m.event(ctx, ops, "expire", l, nil)
				expired++
			}
		case StateExpired, StateReleased:
			if !l.Expires.Add(m.cfg.ExpiredGrace).After(now) {
				m.removeLocked(ip)
				ops = append(ops, Delete(ip))
				purged++
			}
		case StateDeclined:
			if !l.Expires.After(now) {
				m.removeLocked(ip)
				ops = append(ops, Delete(ip))
				purged++
			}
		}
	}
	for ip, o := range m.offers {
		if !o.expires.After(now) {
			delete(m.offers, ip)
			if m.offerBy[offerKey{o.key, o.scopeID}] == o {
				delete(m.offerBy, offerKey{o.key, o.scopeID})
			}
		}
	}
	m.mu.Unlock()
	if len(ops) == 0 {
		return
	}
	if err := m.db.Apply(ctx, ops...); err != nil {
		m.log.Error("persisting lease expiry", "err", err)
		return
	}
	m.log.Debug("lease expiry run", "expired", expired, "purged", purged)
}

// ScopesChanged drops in-memory state of scopes that no longer exist and
// of leases outside their scope's subnet (the database removes them via ON
// DELETE CASCADE, or the HA configuration sync removes them).
func (m *Manager) ScopesChanged(snap *scope.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, ls := range m.byScope {
		p := snap.ByID(id)
		for ip := range ls {
			if p == nil || !p.InSubnet(ip) {
				m.removeLocked(ip)
			}
		}
		if p != nil {
			continue
		}
		for k := range m.cursor {
			if k.scopeID == id {
				delete(m.cursor, k)
			}
		}
	}
	for k, o := range m.offerBy {
		if snap.ByID(k.scopeID) == nil {
			delete(m.offerBy, k)
			delete(m.offers, o.ip)
		}
	}
}

// ---- queries -------------------------------------------------------------------

// Get returns a copy of the lease record for ip.
func (m *Manager) Get(ip uint32) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.leases[ip]
	if l == nil {
		return Lease{}, false
	}
	return *l, true
}

// ByClient returns copies of all records of a client, sorted by IP.
func (m *Manager) ByClient(c Client) []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Lease
	for _, l := range m.byClient[c.Key()] {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// All returns copies of all lease records sorted by IP.
func (m *Manager) All() []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Lease, 0, len(m.leases))
	for _, l := range m.leases {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// Delete removes the lease record for ip (administrative action). The
// address becomes free immediately. It returns false if there is no record.
func (m *Manager) Delete(ctx context.Context, ip uint32) (bool, error) {
	m.mu.Lock()
	l := m.leases[ip]
	if l == nil {
		m.mu.Unlock()
		return false, nil
	}
	_, upd := m.stamps()
	old := *l
	m.removeLocked(ip)
	if o := m.offers[ip]; o != nil {
		delete(m.offers, ip)
		if m.offerBy[offerKey{o.key, o.scopeID}] == o {
			delete(m.offerBy, offerKey{o.key, o.scopeID})
		}
	}
	ops := []Op{Delete(ip)}
	m.metrics.Event("delete")
	if m.cfg.AuditEvents {
		ops = append(ops, AuditOp(audit.New(ctx, "lease.delete", "lease", ip4.String(ip), leaseDetails(&old))))
	}
	m.mu.Unlock()
	if err := m.db.Apply(ctx, ops...); err != nil {
		return true, err
	}
	m.replicate(ctx, false, Change{Deleted: true, Lease: Lease{IP: ip, Updated: upd, Origin: m.cfg.NodeID}})
	return true, nil
}

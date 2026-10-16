package dnsupdate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

// DNS performs the record updates (implemented by *Client).
type DNS interface {
	AddForward(ctx context.Context, t *scope.DNSTarget, name string, ip netip.Addr, dhcid string, ttl uint32) error
	RemoveForward(ctx context.Context, t *scope.DNSTarget, name string, ip netip.Addr, dhcid string) error
	AddReverse(ctx context.Context, t *scope.DNSTarget, ip netip.Addr, name string, ttl uint32) error
	RemoveReverse(ctx context.Context, t *scope.DNSTarget, ip netip.Addr, name string) error
}

// LeaseSource returns the current lease of an address.
type LeaseSource interface {
	Get(ip uint32) (lease.Lease, bool)
}

// SnapshotSource provides the configured zones.
type SnapshotSource interface {
	Snapshot() *scope.Snapshot
}

// Registration states.
const (
	StateRegistered = "registered"
	StatePending    = "pending"
	StateConflict   = "conflict"
	StateError      = "error"
)

// Registration is the DNS state of one leased address: what should be
// registered (Want*) and what is (Have*).
type Registration struct {
	IP       uint32
	ScopeID  int64
	Client   string // lease.ClientKey of the binding
	Cleanup  bool
	TTL      uint32
	WantFQDN string
	WantFwd  bool
	WantRev  bool
	// WantDHCID is set when the scope protects names.
	WantDHCID string
	// HaveFwd is the name of the registered A record (with HaveDHCID),
	// HaveRev the name the PTR record points to; "" = none.
	HaveFwd, HaveDHCID, HaveRev string
	// Conflict: the name belongs to another client; the A record is not
	// registered until the binding changes or is renewed.
	Conflict    bool
	Attempts    int
	NextAttempt time.Time
	LastError   string
	Updated     time.Time

	gen   uint64 // incremented when the wanted state changes
	busy  bool
	dirty bool
}

// fwdWanted reports whether the A record should exist.
func (r *Registration) fwdWanted() bool { return r.WantFwd && r.WantFQDN != "" && !r.Conflict }

func (r *Registration) revWanted() bool { return r.WantRev && r.WantFQDN != "" }

// Synced reports whether DNS matches the wanted state.
func (r *Registration) Synced() bool {
	fwdOK := r.HaveFwd == ""
	if r.fwdWanted() {
		fwdOK = r.HaveFwd == r.WantFQDN && r.HaveDHCID == r.WantDHCID
	}
	revOK := r.HaveRev == ""
	if r.revWanted() {
		revOK = r.HaveRev == r.WantFQDN
	}
	return fwdOK && revOK
}

// idle: nothing wanted, nothing registered.
func (r *Registration) idle() bool {
	return !r.WantFwd && !r.WantRev && r.HaveFwd == "" && r.HaveRev == ""
}

// State summarises the registration for the API and metrics.
func (r *Registration) State() string {
	switch {
	case r.Conflict:
		return StateConflict
	case r.Synced():
		return StateRegistered
	case r.Attempts > 0:
		return StateError
	}
	return StatePending
}

// Updater keeps DNS in line with the leases: Bound records the wanted
// state when a lease is acknowledged, a background worker sends the updates
// (with retries) and a periodic sweep removes the records of leases that
// ended (release, expiry, deletion). The state is persisted in
// dns_registrations, so pending removals survive a restart.
type Updater struct {
	log     *slog.Logger
	db      *store.DB
	snaps   SnapshotSource
	leases  LeaseSource
	dns     DNS
	metrics *metrics.DNS
	now     func() time.Time

	// SweepInterval is the period of the lease check.
	SweepInterval time.Duration
	// Workers is the number of concurrent updates.
	Workers int
	// MaxRemoveAttempts bounds the retries of a removal before the
	// registration is dropped.
	MaxRemoveAttempts int

	mu      sync.Mutex
	regs    map[uint32]*Registration
	deleted []uint32 // rows to delete on the next persist
	wake    chan struct{}
}

// NewUpdater creates an updater. dns is usually NewClient().
func NewUpdater(log *slog.Logger, db *store.DB, snaps SnapshotSource, leases LeaseSource, dns DNS) *Updater {
	return &Updater{
		log: log, db: db, snaps: snaps, leases: leases, dns: dns, now: time.Now,
		SweepInterval: 15 * time.Second, Workers: 4, MaxRemoveAttempts: 12,
		regs: map[uint32]*Registration{}, wake: make(chan struct{}, 1),
	}
}

// SetMetrics enables metrics.
func (u *Updater) SetMetrics(m *metrics.DNS) { u.metrics = m }

// SetClock overrides the time source (tests).
func (u *Updater) SetClock(now func() time.Time) { u.now = now }

func (u *Updater) kick() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Bound records the DNS state wanted for an acknowledged lease. It does no
// I/O and returns immediately.
func (u *Updater) Bound(l lease.Lease, s scope.DNSSettings, d Decision, leaseTime time.Duration) {
	var fqdn, dhcid string
	fwd, rev := false, false
	if d.Updates() {
		fqdn, fwd, rev = d.FQDN, d.Forward, d.Reverse
		if fwd && s.Protect {
			dhcid = DHCID(l.MAC, l.ClientID, fqdn)
		}
	}
	client := string(l.Key())
	ttl := RecordTTL(s, leaseTime)

	u.mu.Lock()
	r := u.regs[l.IP]
	if r == nil {
		if fqdn == "" {
			u.mu.Unlock()
			return
		}
		r = &Registration{IP: l.IP}
		u.regs[l.IP] = r
	}
	changed := r.WantFQDN != fqdn || r.WantFwd != fwd || r.WantRev != rev || r.WantDHCID != dhcid || r.Client != client
	if !changed && !r.Conflict && r.ScopeID == l.ScopeID && r.Cleanup == s.Cleanup && r.TTL == ttl {
		u.mu.Unlock()
		return
	}
	r.ScopeID, r.Client, r.Cleanup, r.TTL = l.ScopeID, client, s.Cleanup, ttl
	if changed || r.Conflict {
		// A renewal retries a conflicting name (it may have been freed).
		r.WantFQDN, r.WantFwd, r.WantRev, r.WantDHCID = fqdn, fwd, rev, dhcid
		r.Conflict, r.Attempts, r.NextAttempt, r.LastError = false, 0, time.Time{}, ""
		r.gen++
	}
	r.dirty = true
	u.mu.Unlock()
	u.kick()
}

// Registrations returns copies of all registrations ordered by address.
func (u *Updater) Registrations() []Registration {
	u.mu.Lock()
	out := make([]Registration, 0, len(u.regs))
	for _, r := range u.regs {
		out = append(out, *r)
	}
	u.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// Run loads the persisted state and processes updates until ctx ends.
func (u *Updater) Run(ctx context.Context) error {
	if err := u.load(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("loading DNS registrations: %w", err)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var nextSweep time.Time
	for {
		now := u.now()
		if !now.Before(nextSweep) {
			u.sweep(now)
			nextSweep = now.Add(u.SweepInterval)
		}
		u.process(ctx)
		u.persist(ctx)
		u.updateMetrics()
		select {
		case <-ctx.Done():
			// Keep the results of the last round.
			u.persist(context.WithoutCancel(ctx))
			return nil
		case <-u.wake:
		case <-tick.C:
		}
	}
}

// sweep withdraws the wanted records of leases that are no longer bound
// to the registered client (or forgets them when the scope keeps records).
func (u *Updater) sweep(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for ip, r := range u.regs {
		if !r.WantFwd && !r.WantRev {
			continue
		}
		if l, ok := u.leases.Get(ip); ok && l.ActiveAt(now) && string(l.Key()) == r.Client {
			continue
		}
		if !r.Cleanup {
			u.log.Debug("lease ended, keeping DNS records", "ip", ip4.String(ip), "fqdn", r.WantFQDN)
			u.forget(ip)
			continue
		}
		r.WantFwd, r.WantRev, r.Conflict = false, false, false
		r.Attempts, r.NextAttempt, r.LastError = 0, time.Time{}, ""
		r.gen++
		r.dirty = true
	}
}

// forget drops a registration (u.mu held); its row is deleted on the next
// persist.
func (u *Updater) forget(ip uint32) {
	delete(u.regs, ip)
	u.deleted = append(u.deleted, ip)
}

func backoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// process runs the due updates (at most Workers at a time) and waits for
// them.
func (u *Updater) process(ctx context.Context) {
	now := u.now()
	u.mu.Lock()
	var due []Registration
	for _, r := range u.regs {
		if !r.busy && !r.Synced() && !now.Before(r.NextAttempt) {
			r.busy = true
			due = append(due, *r)
		}
	}
	u.mu.Unlock()
	if len(due) == 0 {
		return
	}
	snap := u.snaps.Snapshot()
	sem := make(chan struct{}, max(u.Workers, 1))
	var wg sync.WaitGroup
	for i := range due {
		sem <- struct{}{}
		wg.Add(1)
		go func(r Registration) {
			defer func() { <-sem; wg.Done() }()
			res := u.reconcile(ctx, snap, r)
			u.finish(res)
		}(due[i])
	}
	wg.Wait()
}

// finish stores the result of a reconcile run.
func (u *Updater) finish(res Registration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.regs[res.IP]
	if r == nil {
		return
	}
	r.busy, r.dirty = false, true
	// What is registered in DNS is a fact, whatever happened meanwhile.
	r.HaveFwd, r.HaveDHCID, r.HaveRev = res.HaveFwd, res.HaveDHCID, res.HaveRev
	r.Updated = res.Updated
	if r.gen != res.gen {
		// The wanted state changed while the update ran: start over.
		r.Attempts, r.NextAttempt = 0, time.Time{}
		return
	}
	r.Conflict, r.Attempts, r.NextAttempt, r.LastError = res.Conflict, res.Attempts, res.NextAttempt, res.LastError
	switch {
	case r.Synced() && r.idle():
		u.forget(r.IP)
	case !r.Synced() && !r.WantFwd && !r.WantRev && r.Attempts >= u.MaxRemoveAttempts:
		u.log.Error("giving up removing DNS records", "ip", ip4.String(r.IP), "a", r.HaveFwd, "ptr", r.HaveRev, "err", r.LastError)
		u.forget(r.IP)
	}
}

// reconcile sends the updates that bring DNS to the wanted state.
func (u *Updater) reconcile(ctx context.Context, snap *scope.Snapshot, r Registration) Registration {
	ip := ip4.ToAddr(r.IP)
	ipStr := ip.String()
	var errs []string
	fail := func(record, op string, err error) {
		u.metrics.Update(record, op, "error")
		errs = append(errs, err.Error())
		u.log.Warn("DNS update failed", "ip", ipStr, "record", record, "operation", op, "err", err)
	}
	target := func(name string) *scope.DNSTarget { return snap.DNSTargetFor(name) }

	// 1. Withdraw records that are no longer wanted.
	fwdCurrent := r.fwdWanted() && r.HaveFwd == r.WantFQDN && r.HaveDHCID == r.WantDHCID
	if r.HaveFwd != "" && !fwdCurrent {
		if t := target(r.HaveFwd); t == nil {
			u.log.Warn("no DNS zone configured any more; forgetting A record", "ip", ipStr, "fqdn", r.HaveFwd)
			r.HaveFwd, r.HaveDHCID = "", ""
		} else if err := u.dns.RemoveForward(ctx, t, r.HaveFwd, ip, r.HaveDHCID); err != nil {
			fail("A", "remove", err)
		} else {
			u.metrics.Update("A", "remove", "success")
			u.log.Info("DNS A record removed", "ip", ipStr, "fqdn", r.HaveFwd)
			r.HaveFwd, r.HaveDHCID = "", ""
		}
	}
	if r.HaveRev != "" && !r.revWanted() {
		rev := ReverseName(ip)
		if t := target(rev); t == nil {
			u.log.Warn("no DNS zone configured any more; forgetting PTR record", "ip", ipStr, "ptr", rev)
			r.HaveRev = ""
		} else if err := u.dns.RemoveReverse(ctx, t, ip, r.HaveRev); err != nil {
			fail("PTR", "remove", err)
		} else {
			u.metrics.Update("PTR", "remove", "success")
			u.log.Info("DNS PTR record removed", "ip", ipStr, "fqdn", r.HaveRev)
			r.HaveRev = ""
		}
	}
	// 2. Register what is missing.
	if r.fwdWanted() && (r.HaveFwd != r.WantFQDN || r.HaveDHCID != r.WantDHCID) {
		if t := target(r.WantFQDN); t == nil {
			fail("A", "add", fmt.Errorf("no DNS zone configured for %s", r.WantFQDN))
		} else {
			err := u.dns.AddForward(ctx, t, r.WantFQDN, ip, r.WantDHCID, r.TTL)
			switch {
			case errors.Is(err, ErrConflict):
				u.metrics.Update("A", "add", "conflict")
				r.Conflict = true
				r.LastError = fmt.Sprintf("%s is in use by another client or was created manually", r.WantFQDN)
				u.log.Warn("DNS name protection: name belongs to another client, not registered", "ip", ipStr, "fqdn", r.WantFQDN, "client", r.Client)
				u.auditConflict(ctx, r)
			case err != nil:
				fail("A", "add", err)
			default:
				u.metrics.Update("A", "add", "success")
				u.log.Info("DNS A record registered", "ip", ipStr, "fqdn", r.WantFQDN, "ttl", r.TTL)
				r.HaveFwd, r.HaveDHCID = r.WantFQDN, r.WantDHCID
			}
		}
	}
	if r.revWanted() && r.HaveRev != r.WantFQDN {
		rev := ReverseName(ip)
		if t := target(rev); t == nil {
			fail("PTR", "add", fmt.Errorf("no DNS zone configured for %s", rev))
		} else if err := u.dns.AddReverse(ctx, t, ip, r.WantFQDN, r.TTL); err != nil {
			fail("PTR", "add", err)
		} else {
			u.metrics.Update("PTR", "add", "success")
			u.log.Info("DNS PTR record registered", "ip", ipStr, "fqdn", r.WantFQDN)
			r.HaveRev = r.WantFQDN
		}
	}
	r.Updated = u.now()
	if len(errs) > 0 {
		r.Attempts++
		r.NextAttempt = r.Updated.Add(backoff(r.Attempts))
		r.LastError = strings.Join(errs, "; ")
	} else {
		r.Attempts, r.NextAttempt = 0, time.Time{}
		if !r.Conflict {
			r.LastError = ""
		}
	}
	return r
}

func (u *Updater) auditConflict(ctx context.Context, r Registration) {
	ctx = audit.WithActor(ctx, audit.ActorDHCP, "dns")
	e := audit.New(ctx, "dns.conflict", "lease", ip4.String(r.IP), map[string]any{"fqdn": r.WantFQDN, "client": r.Client})
	if err := u.db.WriteTx(ctx, func(tx *sql.Tx) error { return audit.Insert(ctx, tx, e) }); err != nil {
		u.log.Error("writing audit entry", "err", err)
	}
}

func (u *Updater) updateMetrics() {
	if u.metrics == nil {
		return
	}
	counts := map[string]int{}
	u.mu.Lock()
	for _, r := range u.regs {
		counts[r.State()]++
	}
	u.mu.Unlock()
	u.metrics.Registrations(counts)
}

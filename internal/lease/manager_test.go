package lease

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeProber struct {
	mu    sync.Mutex
	inUse map[uint32]bool
	calls []uint32
}

func (f *fakeProber) InUse(_ context.Context, ip uint32, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ip)
	return f.inUse[ip]
}

type env struct {
	t      *testing.T
	db     *store.DB
	scopes *scope.Store
	m      *Manager
	clock  *fakeClock
	prober *fakeProber
	cancel context.CancelFunc
	done   chan error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, scopes: scope.NewStore(db), clock: &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}, prober: &fakeProber{inUse: map[uint32]bool{}}}
	if err := e.scopes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	t.Cleanup(func() {
		e.stop()
		_ = db.Close()
	})
	return e
}

func (e *env) start() {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := DefaultConfig()
	e.m = NewManager(cfg, log, NewDBStore(e.db), e.prober)
	e.m.SetClock(e.clock.Now)
	if err := e.m.Load(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.scopes.OnChange(e.m.ScopesChanged)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan error, 1)
	go func() { e.done <- e.m.Run(ctx) }()
}

func (e *env) stop() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
		e.cancel = nil
	}
}

func (e *env) scope(name, cidr, start, end string) *scope.Pool {
	e.t.Helper()
	sc, err := e.scopes.CreateScope(context.Background(), scope.Scope{
		Name: name, Subnet: netip.MustParsePrefix(cidr),
		Start: netip.MustParseAddr(start), End: netip.MustParseAddr(end),
		LeaseTime: time.Hour, Enabled: true,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.scopes.Snapshot().ByID(sc.ID)
}

func (e *env) pool(id int64) *scope.Pool { return e.scopes.Snapshot().ByID(id) }

func cands(ps ...*scope.Pool) []Candidate {
	out := make([]Candidate, len(ps))
	for i, p := range ps {
		out[i] = PoolCandidate(p)
	}
	return out
}

func ip(s string) uint32 { return ip4.MustFromAddr(netip.MustParseAddr(s)) }

func client(n int) Client {
	return Client{MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", n>>8, n&0xff)}
}

// dora runs DISCOVER + REQUEST and returns the bound address.
func (e *env) dora(c Client, p *scope.Pool) uint32 {
	e.t.Helper()
	ctx := context.Background()
	o := e.m.Offer(ctx, c, cands(p), 0)
	if o == nil {
		e.t.Fatalf("no offer for %s", c.MAC)
	}
	res, err := e.m.Request(ctx, c, PoolCandidate(o.Pool), o.IP)
	if err != nil || !res.Ack {
		e.t.Fatalf("request failed: %+v %v", res, err)
	}
	return res.Lease.IP
}

func TestOfferRequestAndPersistence(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	c := client(1)
	got := e.dora(c, p)
	if got != ip("10.0.0.10") {
		t.Fatalf("first address = %s", ip4.String(got))
	}
	l, ok := e.m.Get(got)
	if !ok || l.State != StateActive || !l.Expires.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("lease = %+v", l)
	}
	// Same client gets the same address again.
	if again := e.dora(c, p); again != got {
		t.Fatalf("renew gave %s", ip4.String(again))
	}
	// Restart: lease survives.
	e.stop()
	e.start()
	if l, ok := e.m.Get(got); !ok || l.MAC != c.MAC || l.State != StateActive {
		t.Fatalf("lease lost after restart: %+v", l)
	}
	if again := e.dora(c, p); again != got {
		t.Fatal("affinity lost after restart")
	}
}

func TestExclusionsAndReservationPreferred(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	if _, err := e.scopes.AddExclusion(ctx, scope.Exclusion{ScopeID: p.ID, Start: netip.MustParseAddr("10.0.0.10"), End: netip.MustParseAddr("10.0.0.12")}); err != nil {
		t.Fatal(err)
	}
	c := client(7)
	if _, err := e.scopes.CreateReservation(ctx, scope.Reservation{ScopeID: p.ID, IP: netip.MustParseAddr("10.0.0.100"), MAC: c.MAC}); err != nil {
		t.Fatal(err)
	}
	p = e.pool(p.ID)
	if got := e.dora(client(1), p); got != ip("10.0.0.13") {
		t.Fatalf("exclusion ignored: %s", ip4.String(got))
	}
	if got := e.dora(c, p); got != ip("10.0.0.100") {
		t.Fatalf("reservation not preferred: %s", ip4.String(got))
	}
	// The reserved client asking for another address gets a NAK.
	res, err := e.m.Request(ctx, c, PoolCandidate(p), ip("10.0.0.14"))
	if err != nil || res.Ack {
		t.Fatalf("reserved client got other address: %+v", res)
	}
	// Another client may not take the reserved address.
	res, _ = e.m.Request(ctx, client(2), PoolCandidate(p), ip("10.0.0.100"))
	if res.Ack {
		t.Fatal("reserved address given to wrong client")
	}
	// Excluded address cannot be requested.
	res, _ = e.m.Request(ctx, client(2), PoolCandidate(p), ip("10.0.0.11"))
	if res.Ack {
		t.Fatal("excluded address acknowledged")
	}
}

func TestRequestedAddressAndNak(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	o := e.m.Offer(ctx, client(1), cands(p), ip("10.0.0.15"))
	if o == nil || o.IP != ip("10.0.0.15") {
		t.Fatalf("requested address not honoured: %+v", o)
	}
	// Address offered to client 1 cannot be requested by client 2.
	if res, _ := e.m.Request(ctx, client(2), PoolCandidate(p), ip("10.0.0.15")); res.Ack {
		t.Fatal("offered address taken by another client")
	}
	if res, _ := e.m.Request(ctx, client(1), PoolCandidate(p), ip("10.0.0.15")); !res.Ack {
		t.Fatalf("NAK: %s", res.Reason)
	}
	// INIT-REBOOT for a free, valid address of an unknown client is accepted.
	if res, _ := e.m.Request(ctx, client(3), PoolCandidate(p), ip("10.0.0.18")); !res.Ack {
		t.Fatalf("free address not acknowledged: %s", res.Reason)
	}
	// ...but not for an address outside the range.
	if res, _ := e.m.Request(ctx, client(3), PoolCandidate(p), ip("10.0.0.50")); res.Ack {
		t.Fatal("out-of-range address acknowledged")
	}
	// A client gets only one active binding per scope.
	leases := e.m.ByClient(client(3))
	if len(leases) != 1 || leases[0].IP != ip("10.0.0.18") {
		t.Fatalf("leases of client 3: %+v", leases)
	}
}

func TestReleaseDeclineAndExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.12")
	a, b := client(1), client(2)
	ipA := e.dora(a, p)

	ok, err := e.m.Release(ctx, b, ipA)
	if err != nil || ok {
		t.Fatal("foreign release accepted")
	}
	if ok, _ := e.m.Release(ctx, a, ipA); !ok {
		t.Fatal("release rejected")
	}
	if l, _ := e.m.Get(ipA); l.State != StateReleased {
		t.Fatalf("state = %s", l.State)
	}
	// Released addresses are only reused after untouched ones.
	if got := e.dora(b, p); got == ipA {
		t.Fatal("released address reused while free ones exist")
	}
	// Client a comes back and gets its address again (affinity).
	if got := e.dora(a, p); got != ipA {
		t.Fatalf("affinity: got %s", ip4.String(got))
	}

	// Decline blocks the address.
	ipB, _ := e.m.Get(ip("10.0.0.11"))
	if ok, _ := e.m.Decline(ctx, b, p, ipB.IP); !ok {
		t.Fatal("decline rejected")
	}
	if l, _ := e.m.Get(ipB.IP); l.State != StateDeclined {
		t.Fatalf("state after decline = %s", l.State)
	}
	got := e.dora(b, p)
	if got == ipB.IP {
		t.Fatal("declined address offered again")
	}

	// Pool is now full: a, b, declined.
	if o := e.m.Offer(ctx, client(9), cands(p), 0); o != nil {
		t.Fatalf("offer from exhausted pool: %s", ip4.String(o.IP))
	}

	// Leases expire, are kept during grace, then purged.
	e.clock.Advance(time.Hour + time.Second)
	e.m.Expire(ctx)
	if l, _ := e.m.Get(ipA); l.State != StateExpired {
		t.Fatalf("state after expiry = %s", l.State)
	}
	// Exhausted pool: oldest expired record is reused for a new client.
	if o := e.m.Offer(ctx, client(9), cands(p), 0); o == nil {
		t.Fatal("expired addresses must be reusable when the pool is exhausted")
	}
	e.clock.Advance(DefaultConfig().ExpiredGrace)
	e.m.Expire(ctx)
	if _, ok := e.m.Get(ipA); ok {
		t.Fatal("expired lease not purged after grace period")
	}
	e.clock.Advance(DefaultConfig().DeclineTime)
	e.m.Expire(ctx)
	if _, ok := e.m.Get(ipB.IP); ok {
		t.Fatal("declined record not purged")
	}
	// Purge is persisted.
	e.stop()
	e.start()
	if len(e.m.All()) != 0 {
		t.Fatalf("records after restart: %+v", e.m.All())
	}
}

func TestExpiredAddressHeldForPreviousClient(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.11")
	a := client(1)
	ipA := e.dora(a, p)
	e.clock.Advance(2 * time.Hour)
	e.m.Expire(ctx)
	// Another client asking explicitly for a's address is refused during grace.
	if res, _ := e.m.Request(ctx, client(2), PoolCandidate(p), ipA); res.Ack {
		t.Fatal("expired address handed to another client during grace")
	}
	// The original client gets it back.
	if got := e.dora(a, p); got != ipA {
		t.Fatal("previous client did not get its address back")
	}
}

func TestConflictDetection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	e.prober.inUse[ip("10.0.0.10")] = true
	c := client(1)
	o := e.m.Offer(ctx, c, cands(p), 0)
	if o == nil || o.IP != ip("10.0.0.11") {
		t.Fatalf("conflicting address offered: %+v", o)
	}
	if l, ok := e.m.Get(ip("10.0.0.10")); !ok || l.State != StateDeclined {
		t.Fatalf("conflict not recorded: %+v", l)
	}
	// Repeated DISCOVER re-uses the probed offer without probing again.
	n := len(e.prober.calls)
	if o2 := e.m.Offer(ctx, c, cands(p), 0); o2 == nil || o2.IP != o.IP {
		t.Fatal("offer not stable")
	}
	if len(e.prober.calls) != n {
		t.Fatal("address probed twice for the same client")
	}
	// Renewal of an active lease is not probed.
	if _, err := e.m.Request(ctx, c, PoolCandidate(p), o.IP); err != nil {
		t.Fatal(err)
	}
	n = len(e.prober.calls)
	e.m.Offer(ctx, c, cands(p), 0)
	if len(e.prober.calls) != n {
		t.Fatal("active lease probed")
	}
}

func TestClientIDIdentity(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	// Same MAC, different client identifiers = different clients.
	a := Client{MAC: "02:00:00:00:00:01", ClientID: "01aa"}
	b := Client{MAC: "02:00:00:00:00:01", ClientID: "01bb"}
	if e.dora(a, p) == e.dora(b, p) {
		t.Fatal("client identifiers not distinguished")
	}
}

func TestScopeDeletionDropsLeases(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	e.dora(client(1), p)
	if err := e.scopes.DeleteScope(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.m.All()) != 0 {
		t.Fatal("leases of deleted scope kept in memory")
	}
}

func TestDisabledScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	c := client(1)
	got := e.dora(c, p)
	sc := p.Scope
	sc.Enabled = false
	if _, err := e.scopes.UpdateScope(ctx, sc); err != nil {
		t.Fatal(err)
	}
	p = e.pool(p.ID)
	if o := e.m.Offer(ctx, client(2), cands(p), 0); o != nil {
		t.Fatal("offer from disabled scope")
	}
	if res, _ := e.m.Request(ctx, c, PoolCandidate(p), got); res.Ack {
		t.Fatal("renewal in disabled scope acknowledged")
	}
}

func TestNoDuplicatesUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/16", "10.0.1.0", "10.0.8.255")
	const n = 1000
	var wg sync.WaitGroup
	results := make([]uint32, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			c := client(i)
			unlock := e.m.LockClient(c)
			defer unlock()
			ctx := context.Background()
			o := e.m.Offer(ctx, c, cands(p), 0)
			if o == nil {
				errs <- fmt.Errorf("client %d: no offer", i)
				return
			}
			res, err := e.m.Request(ctx, c, PoolCandidate(o.Pool), o.IP)
			if err != nil || !res.Ack {
				errs <- fmt.Errorf("client %d: %w %s", i, err, res.Reason)
				return
			}
			results[i] = res.Lease.IP
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[uint32]int{}
	for i, a := range results {
		if prev, dup := seen[a]; dup {
			t.Fatalf("duplicate %s for clients %d and %d", ip4.String(a), prev, i)
		}
		seen[a] = i
	}
	var count int
	if err := e.db.R.QueryRow(`SELECT COUNT(*) FROM leases WHERE state = 'active'`).Scan(&count); err != nil || count != n {
		t.Fatalf("persisted active leases = %d (%v)", count, err)
	}
}

func TestCandidateRangesAndLeaseTime(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	phones := Candidate{Pool: p, Ranges: []ip4.Range{{Start: ip("10.0.0.15"), End: ip("10.0.0.16")}}, LeaseTime: 5 * time.Minute}
	for i := range 2 {
		c := client(i)
		o := e.m.Offer(ctx, c, []Candidate{phones}, 0)
		if o == nil || o.IP < ip("10.0.0.15") || o.IP > ip("10.0.0.16") || o.LeaseTime != 5*time.Minute {
			t.Fatalf("offer outside policy range: %+v", o)
		}
		res, err := e.m.Request(ctx, c, phones, o.IP)
		if err != nil || !res.Ack || !res.Lease.Expires.Equal(e.clock.Now().Add(5*time.Minute)) || res.LeaseTime != 5*time.Minute {
			t.Fatalf("request: %+v %v", res, err)
		}
	}
	// Policy range exhausted: no fallback to the general range.
	if o := e.m.Offer(ctx, client(3), []Candidate{phones}, 0); o != nil {
		t.Fatalf("offer from exhausted policy range: %s", ip4.String(o.IP))
	}
	// Requesting an address outside the allowed ranges is refused.
	if res, _ := e.m.Request(ctx, client(3), phones, ip("10.0.0.11")); res.Ack {
		t.Fatal("address outside policy range acknowledged")
	}
	// Empty (non-nil) ranges allow nothing dynamic.
	none := Candidate{Pool: p, Ranges: []ip4.Range{}}
	if o := e.m.Offer(ctx, client(4), []Candidate{none}, 0); o != nil {
		t.Fatal("offer although no range is allowed")
	}
}

func TestScopeStats(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.29") // 20 addresses
	if _, err := e.scopes.AddExclusion(ctx, scope.Exclusion{ScopeID: p.ID, Start: netip.MustParseAddr("10.0.0.10"), End: netip.MustParseAddr("10.0.0.13")}); err != nil {
		t.Fatal(err)
	}
	// One reservation inside the range (unused), one outside (used).
	if _, err := e.scopes.CreateReservation(ctx, scope.Reservation{ScopeID: p.ID, IP: netip.MustParseAddr("10.0.0.20"), MAC: "aa:00:00:00:00:01"}); err != nil {
		t.Fatal(err)
	}
	outside := Client{MAC: "aa:00:00:00:00:02"}
	if _, err := e.scopes.CreateReservation(ctx, scope.Reservation{ScopeID: p.ID, IP: netip.MustParseAddr("10.0.0.200"), MAC: outside.MAC}); err != nil {
		t.Fatal(err)
	}
	p = e.pool(p.ID)
	a := e.dora(client(1), p)
	e.dora(client(2), p)
	e.dora(outside, p)
	if ok, _ := e.m.Decline(ctx, client(2), p, ip("10.0.0.15")); !ok {
		t.Fatal("decline")
	}
	if ok, _ := e.m.Release(ctx, client(1), a); !ok {
		t.Fatal("release")
	}
	e.dora(client(3), p)

	st := e.m.ScopeStats(p)
	want := ScopeStats{Total: 20, Excluded: 4, Usable: 16, InUse: 1, Reserved: 1, Declined: 1, Available: 13, ActiveLeases: 2}
	if st.Total != want.Total || st.Excluded != want.Excluded || st.Usable != want.Usable || st.InUse != want.InUse ||
		st.Reserved != want.Reserved || st.Declined != want.Declined || st.Available != want.Available || st.ActiveLeases != want.ActiveLeases {
		t.Fatalf("stats = %+v", st)
	}
	if st.ByState[StateActive] != 2 || st.ByState[StateReleased] != 1 || st.ByState[StateDeclined] != 1 {
		t.Fatalf("by state = %v", st.ByState)
	}
	if got := st.Utilization; got < 18.74 || got > 18.76 {
		t.Fatalf("utilization = %v", got)
	}
	if all := e.m.Stats(e.scopes.Snapshot()); len(all) != 1 || all[0].Name != "lan" {
		t.Fatalf("Stats() = %+v", all)
	}
}

func TestLeaseEventsAudited(t *testing.T) {
	e := newEnv(t)
	ctx := audit.WithActor(context.Background(), audit.ActorDHCP, "eth0")
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	log := audit.NewLog(e.db)
	before, _ := log.Count(ctx)

	c := client(1)
	o := e.m.Offer(ctx, c, cands(p), 0)
	if _, err := e.m.Request(ctx, c, PoolCandidate(p), o.IP); err != nil { // assign
		t.Fatal(err)
	}
	if _, err := e.m.Request(ctx, c, PoolCandidate(p), o.IP); err != nil { // renew (not audited by default)
		t.Fatal(err)
	}
	if _, err := e.m.Release(ctx, c, o.IP); err != nil {
		t.Fatal(err)
	}
	d := client(2)
	o = e.m.Offer(ctx, d, cands(p), 0)
	if _, err := e.m.Decline(ctx, d, p, o.IP); err != nil {
		t.Fatal(err)
	}
	f := client(3)
	o = e.m.Offer(ctx, f, cands(p), 0)
	if _, err := e.m.Request(ctx, f, PoolCandidate(p), o.IP); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour)
	e.m.Expire(context.Background())

	entries, err := log.List(ctx, audit.Filter{Action: "lease."})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(entries) - 1; i >= 0; i-- {
		actions = append(actions, entries[i].Action)
	}
	got := strings.Join(actions, ",")
	if got != "lease.assign,lease.release,lease.decline,lease.assign,lease.expire" {
		t.Fatalf("audited lease events: %s", got)
	}
	if entries[len(entries)-1].Actor != audit.ActorDHCP || entries[len(entries)-1].Source != "eth0" {
		t.Fatalf("actor/source: %+v", entries[len(entries)-1])
	}
	if entries[0].Actor != audit.ActorSystem {
		t.Fatalf("expiry actor: %+v", entries[0])
	}
	if after, _ := log.Count(ctx); after-before != int64(len(entries)) {
		t.Fatal("unexpected extra audit entries")
	}
}

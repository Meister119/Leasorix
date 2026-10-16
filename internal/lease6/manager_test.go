package lease6

import (
	"context"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/ip6"
	"github.com/meister119/leasorix/internal/scope6"
	"github.com/meister119/leasorix/internal/store"
)

type env struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	scopes *scope6.Store
	m      *Manager
	now    time.Time
}

func newEnv(t *testing.T, sc scope6.Scope) (*env, *scope6.Pool) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	s := scope6.NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateScope(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, db: db, scopes: s, now: time.Unix(1_800_000_000, 0)}
	e.m = e.manager()
	return e, s.Snapshot().ByID(created.ID)
}

func (e *env) manager() *Manager {
	m := NewManager(DefaultConfig(), slog.New(slog.DiscardHandler), e.db)
	m.SetClock(func() time.Time { return e.now })
	if err := m.Load(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func smallScope() scope6.Scope {
	return scope6.Scope{
		Name: "lan6", Prefix: netip.MustParsePrefix("2001:db8:1::/64"),
		Start: netip.MustParseAddr("2001:db8:1::10"), End: netip.MustParseAddr("2001:db8:1::13"),
		PreferredLifetime: time.Hour, ValidLifetime: 2 * time.Hour, Enabled: true,
		PDPool: netip.MustParsePrefix("2001:db8:100::/62"), PDLength: 64,
	}
}

func na(duid string, iaid uint32) Client { return Client{DUID: duid, IAID: iaid, Kind: KindNA} }

func (e *env) assign(c Client, p *scope6.Pool, commit bool, hints ...netip.Prefix) (Lease, bool) {
	e.t.Helper()
	l, ok, err := e.m.Assign(e.ctx, Request{Client: c, Pool: p, Hints: hints}, commit)
	if err != nil {
		e.t.Fatal(err)
	}
	return l, ok
}

func TestAssignAdvertiseCommitAndStability(t *testing.T) {
	e, p := newEnv(t, smallScope())
	c := na("aabbcc01", 1)
	adv, ok := e.assign(c, p, false)
	if !ok || !p.InRange(adv.Addr()) {
		t.Fatalf("advertise %+v %v", adv, ok)
	}
	// The advertised address is held for the client.
	other, ok := e.assign(na("aabbcc02", 1), p, false)
	if !ok || other.Prefix == adv.Prefix {
		t.Fatalf("held address given to another client: %v", other.Prefix)
	}
	got, ok := e.assign(c, p, true)
	if !ok || got.Prefix != adv.Prefix || !got.Expires.Equal(e.now.Add(2*time.Hour)) || !got.Preferred.Equal(e.now.Add(time.Hour)) {
		t.Fatalf("commit %+v", got)
	}
	// Persisted: a restarted manager knows the binding.
	e.m = e.manager()
	if l, ok := e.m.Get(got.Prefix); !ok || l.DUID != "aabbcc01" || l.State != StateActive {
		t.Fatalf("after restart %+v %v", l, ok)
	}
	// Release, expiry of the record and a new request: same address.
	if ok, _ := e.m.Release(e.ctx, c, got.Prefix); !ok {
		t.Fatal("release")
	}
	e.now = e.now.Add(10 * time.Hour)
	e.m.Expire(e.ctx)
	again, _ := e.assign(c, p, true)
	if again.Prefix != got.Prefix {
		t.Fatalf("stable address: %v then %v", got.Prefix, again.Prefix)
	}
}

func TestExhaustionExclusionsReservations(t *testing.T) {
	e, p := newEnv(t, smallScope())
	ctx := e.ctx
	if _, err := e.scopes.AddExclusion(ctx, scope6.Exclusion{ScopeID: p.ID, Start: netip.MustParseAddr("2001:db8:1::10"), End: netip.MustParseAddr("2001:db8:1::10")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.scopes.CreateReservation(ctx, scope6.Reservation{ScopeID: p.ID, DUID: "0000aa", IAID: scope6.AnyIAID, IP: netip.MustParseAddr("2001:db8:1::11")}); err != nil {
		t.Fatal(err)
	}
	p = e.scopes.Snapshot().ByID(p.ID)
	// The reserved client gets its address.
	if l, ok := e.assign(na("0000aa", 9), p, true); !ok || l.Addr().String() != "2001:db8:1::11" {
		t.Fatalf("reservation %+v", l)
	}
	// Only ::12 and ::13 are left for others.
	seen := map[netip.Prefix]bool{}
	for i := range 2 {
		l, ok := e.assign(na("ff00"+string(rune('a'+i))+"0", 1), p, true)
		if !ok || seen[l.Prefix] || l.Addr().String() == "2001:db8:1::10" || l.Addr().String() == "2001:db8:1::11" {
			t.Fatalf("client %d: %+v %v", i, l, ok)
		}
		seen[l.Prefix] = true
	}
	if _, ok := e.assign(na("ffffff", 1), p, true); ok {
		t.Fatal("exhausted scope assigned an address")
	}
	// A declined address stays blocked.
	var any netip.Prefix
	for pf := range seen {
		any = pf
	}
	l, _ := e.m.Get(any)
	if ok, _ := e.m.Decline(ctx, Client{l.DUID, l.IAID, KindNA}, any); !ok {
		t.Fatal("decline")
	}
	if _, ok := e.assign(na("ffffff", 1), p, true); ok {
		t.Fatal("declined address reused")
	}
}

func TestHintsRenewAndPD(t *testing.T) {
	e, p := newEnv(t, smallScope())
	c := na("0102030405", 7)
	hint := netip.MustParsePrefix("2001:db8:1::13/128")
	l, ok := e.assign(c, p, true, hint)
	if !ok || l.Prefix != hint {
		t.Fatalf("hint ignored: %+v", l)
	}
	// Hints outside the range are ignored.
	l2, ok := e.assign(na("0102030406", 7), p, true, netip.MustParsePrefix("2001:db8:1::99/128"))
	if !ok || !p.InRange(l2.Addr()) {
		t.Fatalf("bad hint %+v", l2)
	}
	e.now = e.now.Add(30 * time.Minute)
	ext, ok, err := e.m.Extend(e.ctx, c, p, hint, "pc7")
	if err != nil || !ok || !ext.Expires.Equal(e.now.Add(2*time.Hour)) || ext.Hostname != "pc7" {
		t.Fatalf("extend %+v %v %v", ext, ok, err)
	}
	if _, ok, _ := e.m.Extend(e.ctx, na("0102030406", 7), p, hint, ""); ok {
		t.Fatal("extended another client's binding")
	}

	// Prefix delegation: four /64 in the /62 pool.
	pd := Client{DUID: "0102030405", IAID: 1, Kind: KindPD}
	got := map[netip.Prefix]bool{}
	for i := range 4 {
		c := pd
		c.IAID = uint32(i)
		l, ok := e.assign(c, p, true)
		if !ok || l.Prefix.Bits() != 64 || !p.PDPool.Contains(l.Addr()) || got[l.Prefix] {
			t.Fatalf("pd %d: %+v %v", i, l, ok)
		}
		got[l.Prefix] = true
	}
	if _, ok := e.assign(Client{DUID: "99", IAID: 1, Kind: KindPD}, p, true); ok {
		t.Fatal("pd pool exhausted but assigned")
	}
	st := e.m.Stats(e.scopes.Snapshot())
	if len(st) != 1 || st[0].Active != 2 || st[0].Prefixes != 4 || st[0].Size != 4 || st[0].PrefixCapacity != 4 {
		t.Fatalf("stats %+v", st)
	}
}

func TestLargeRangeAndHASplit(t *testing.T) {
	sc := smallScope()
	sc.Start, sc.End = netip.MustParseAddr("2001:db8:1::"), netip.MustParseAddr("2001:db8:1::ffff:ffff:ffff:ffff")
	e, p := newEnv(t, sc)
	// Only addresses in the lower half of the HA buckets are ours.
	e.m.SetCoordinator(coordFunc(func(pf netip.Prefix) bool { return ip6.Bucket(pf.Addr()) < 50 }))
	seen := map[netip.Prefix]bool{}
	for i := range 200 {
		l, ok := e.assign(na("cafe", uint32(i)), p, true)
		if !ok || seen[l.Prefix] || ip6.Bucket(l.Addr()) >= 50 {
			t.Fatalf("client %d: %+v %v", i, l, ok)
		}
		seen[l.Prefix] = true
	}
}

type coordFunc func(netip.Prefix) bool

func (f coordFunc) CanAllocate6(p netip.Prefix) bool { return f(p) }

func TestExpiryPurgeAndScopeRemoval(t *testing.T) {
	e, p := newEnv(t, smallScope())
	l, _ := e.assign(na("abcdef", 1), p, true)
	e.now = e.now.Add(3 * time.Hour)
	e.m.Expire(e.ctx)
	if got, _ := e.m.Get(l.Prefix); got.State != StateExpired {
		t.Fatalf("not expired: %+v", got)
	}
	e.now = e.now.Add(5 * time.Hour)
	e.m.Expire(e.ctx)
	if _, ok := e.m.Get(l.Prefix); ok {
		t.Fatal("not purged after the grace period")
	}
	var n int
	if err := e.db.R.QueryRow(`SELECT COUNT(*) FROM leases6`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows %d %v", n, err)
	}
	l, _ = e.assign(na("abcdef", 1), p, true)
	e.scopes.OnChange(e.m.ScopesChanged)
	if err := e.scopes.DeleteScope(e.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.m.Get(l.Prefix); ok {
		t.Fatal("binding of a deleted scope kept")
	}
}

package lease

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/ip4"
)

type fakeCoord struct {
	mu       sync.Mutex
	owns     func(uint32) bool
	limit    time.Duration
	failSync bool
	sent     []Change
	critical int
}

func (f *fakeCoord) CanAllocate(ip uint32) bool { return f.owns == nil || f.owns(ip) }

func (f *fakeCoord) LeaseLimit() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limit
}

func (f *fakeCoord) Replicate(_ context.Context, changes []Change, critical bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, changes...)
	if critical {
		f.critical++
		if f.failSync {
			// The partner did not confirm: the coordinator switches to a
			// state with an MCLT limit.
			f.limit = 10 * time.Minute
			return false
		}
		return true
	}
	return false
}

func TestCoordinatorRestrictsAllocationToOwnShare(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.29")
	odd := func(ip uint32) bool { return ip%2 == 1 }
	e.m.SetCoordinator(&fakeCoord{owns: odd})
	for i := range 10 {
		got := e.dora(client(i), p)
		if !odd(got) {
			t.Fatalf("allocated %s from the partner's share", ip4.String(got))
		}
	}
	// The own share is exhausted: no offer although the pool has room.
	if o := e.m.Offer(context.Background(), client(99), cands(p), 0); o != nil {
		t.Fatalf("offer %s from the partner's share", ip4.String(o.IP))
	}
	// A requested free address of the partner's share is refused...
	res, err := e.m.Request(context.Background(), client(98), PoolCandidate(p), ip("10.0.0.12"))
	if err != nil || res.Ack {
		t.Fatalf("request for partner address: %+v %v", res, err)
	}
	// ...but a client keeps an existing binding in the partner's share.
	e.m.SetCoordinator(nil)
	got := e.dora(client(50), p)
	if odd(got) {
		t.Fatalf("expected an even address, got %s", ip4.String(got))
	}
	e.m.SetCoordinator(&fakeCoord{owns: odd})
	res, err = e.m.Request(context.Background(), client(50), PoolCandidate(p), got)
	if err != nil || !res.Ack {
		t.Fatalf("renewal of existing binding refused: %+v %v", res, err)
	}
}

func TestLeaseLimitAndFailedSyncReplication(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	fc := &fakeCoord{limit: 30 * time.Minute}
	e.m.SetCoordinator(fc)
	ctx := context.Background()
	o := e.m.Offer(ctx, client(1), cands(p), 0)
	if o.LeaseTime != 30*time.Minute {
		t.Fatalf("offer lease time %s, want MCLT limit", o.LeaseTime)
	}
	res, err := e.m.Request(ctx, client(1), PoolCandidate(p), o.IP)
	if err != nil || !res.Ack || res.LeaseTime != 30*time.Minute || !res.Lease.Expires.Equal(e.clock.Now().Add(30*time.Minute)) {
		t.Fatalf("limited request: %+v %v", res, err)
	}
	// Normal state (no limit), but the partner does not confirm.
	fc.limit, fc.failSync = 0, true
	res, err = e.m.Request(ctx, client(1), PoolCandidate(p), o.IP)
	if err != nil || !res.Ack || res.LeaseTime != 10*time.Minute {
		t.Fatalf("unconfirmed request must be shortened: %+v %v", res, err)
	}
	if l, _ := e.m.Get(o.IP); !l.Expires.Equal(e.clock.Now().Add(10 * time.Minute)) {
		t.Fatalf("stored lease not shortened: %+v", l)
	}
	e.stop()
	e.start()
	if l, _ := e.m.Get(o.IP); !l.Expires.Equal(e.clock.Now().Add(10*time.Minute)) || l.Origin != "" {
		t.Fatalf("persisted lease: %+v", l)
	}
}

func TestChangesAreReplicated(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	fc := &fakeCoord{}
	e.m.SetCoordinator(fc)
	ctx := context.Background()
	a := e.dora(client(1), p)
	if _, err := e.m.Release(ctx, client(1), a); err != nil {
		t.Fatal(err)
	}
	b := e.dora(client(2), p)
	if ok, err := e.m.Decline(ctx, client(2), p, b); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := e.m.Delete(ctx, b); !ok || err != nil {
		t.Fatal(ok, err)
	}
	var kinds []string
	for _, c := range fc.sent {
		k := string(c.Lease.State)
		if c.Deleted {
			k = "deleted"
		}
		kinds = append(kinds, k)
	}
	want := []string{"active", "released", "active", "declined", "deleted"}
	if len(kinds) != len(want) {
		t.Fatalf("replicated %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("replicated %v, want %v", kinds, want)
		}
	}
	if fc.critical != 2 {
		t.Fatalf("critical replications = %d, want 2 (the bindings)", fc.critical)
	}
}

func TestWinsRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(mac string, st State, exp, upd time.Duration, origin string) *Lease {
		return &Lease{IP: 1, MAC: mac, State: st, Expires: now.Add(exp), Updated: now.Add(upd), Origin: origin}
	}
	cases := []struct {
		name string
		a, b *Lease
		want bool
	}{
		{"same client newer change", mk("x", StateReleased, 0, 0, "a"), mk("x", StateActive, time.Hour, -time.Minute, "b"), true},
		{"same client older change", mk("x", StateActive, time.Hour, -time.Minute, "b"), mk("x", StateReleased, 0, 0, "a"), false},
		{"binding beats expired other client", mk("y", StateActive, time.Hour, -time.Hour, "a"), mk("x", StateExpired, -time.Minute, 0, "b"), true},
		{"conflict: later end wins", mk("y", StateActive, 2*time.Hour, -time.Hour, "a"), mk("x", StateActive, time.Hour, 0, "b"), true},
		{"conflict: tie by node id", mk("y", StateActive, time.Hour, 0, "b"), mk("x", StateActive, time.Hour, 0, "a"), true},
		{"conflict: tie by node id (reverse)", mk("x", StateActive, time.Hour, 0, "a"), mk("y", StateActive, time.Hour, 0, "b"), false},
		{"neither binding: newer wins", mk("y", StateReleased, -time.Hour, time.Second, "a"), mk("x", StateExpired, -time.Minute, 0, "b"), true},
		{"identical record does not win", mk("x", StateActive, time.Hour, 0, "a"), mk("x", StateActive, time.Hour, 0, "a"), false},
	}
	for _, c := range cases {
		if got := Wins(c.a, c.b, now); got != c.want {
			t.Errorf("%s: Wins = %v, want %v", c.name, got, c.want)
		}
		// Determinism: exactly one side wins unless the records are equal.
		if c.name != "identical record does not win" && Wins(c.a, c.b, now) == Wins(c.b, c.a, now) {
			t.Errorf("%s: both or neither win", c.name)
		}
	}
}

func TestApplyRemoteMergesAndPersists(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	ctx := context.Background()
	local := e.dora(client(1), p)
	now := e.clock.Now()
	snap := e.scopes.Snapshot()
	remote := []Change{
		// Newer change of our client's lease (released on the partner).
		{Lease: Lease{IP: local, MAC: client(1).MAC, State: StateReleased, Start: now, Expires: now, Updated: now.Add(time.Second), Origin: "peer"}},
		// New binding of another client.
		{Lease: Lease{IP: ip("10.0.0.15"), ScopeID: 999, MAC: client(2).MAC, State: StateActive, Start: now, Expires: now.Add(time.Hour), Updated: now, Origin: "peer"}},
		// Outside all scopes: ignored.
		{Lease: Lease{IP: ip("192.168.0.1"), MAC: client(3).MAC, State: StateActive, Start: now, Expires: now.Add(time.Hour), Updated: now, Origin: "peer"}},
	}
	applied, conflicts, err := e.m.ApplyRemote(ctx, snap, remote)
	if err != nil || applied != 2 || conflicts != 0 {
		t.Fatalf("applied=%d conflicts=%d err=%v", applied, conflicts, err)
	}
	// Applying the same changes again is a no-op.
	if applied, _, _ := e.m.ApplyRemote(ctx, snap, remote); applied != 0 {
		t.Fatalf("re-apply changed %d records", applied)
	}
	// A conflicting binding with a later end replaces ours.
	mine := e.dora(client(4), p)
	l, _ := e.m.Get(mine)
	confl := Change{Lease: Lease{IP: mine, MAC: client(5).MAC, State: StateActive, Start: now, Expires: l.Expires.Add(time.Minute), Updated: now, Origin: "peer"}}
	if _, conflicts, err := e.m.ApplyRemote(ctx, snap, []Change{confl}); err != nil || conflicts != 1 {
		t.Fatalf("conflicts=%d err=%v", conflicts, err)
	}
	// Deletion.
	del := Change{Deleted: true, Lease: Lease{IP: ip("10.0.0.15"), Updated: now.Add(time.Minute), Origin: "peer"}}
	if applied, _, _ := e.m.ApplyRemote(ctx, snap, []Change{del}); applied != 1 {
		t.Fatal("deletion not applied")
	}
	e.stop()
	e.start()
	if l, _ := e.m.Get(local); l.State != StateReleased || l.Origin != "peer" || !l.Updated.Equal(now.Add(time.Second)) {
		t.Fatalf("merged record after restart: %+v", l)
	}
	if l, _ := e.m.Get(mine); l.MAC != client(5).MAC || l.ScopeID != p.ID {
		t.Fatalf("conflict winner not stored: %+v", l)
	}
	if _, ok := e.m.Get(ip("10.0.0.15")); ok {
		t.Fatal("deleted record survived")
	}
}

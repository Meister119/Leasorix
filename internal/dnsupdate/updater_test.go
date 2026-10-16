package dnsupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

// fakeDNS records the updates and simulates a zone with name protection.
type fakeDNS struct {
	mu    sync.Mutex
	calls []string
	a     map[string]string // name -> ip
	dhcid map[string]string // name -> dhcid
	ptr   map[string]string // ip -> name
	fail  error
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{a: map[string]string{}, dhcid: map[string]string{}, ptr: map[string]string{}}
}

func (f *fakeDNS) log(format string, args ...any) error {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
	return f.fail
}

func (f *fakeDNS) AddForward(_ context.Context, t *scope.DNSTarget, name string, ip netip.Addr, dhcid string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("add A %s %s @%s", name, ip, t.Zone); err != nil {
		return err
	}
	if _, used := f.a[name]; used && (dhcid == "" || f.dhcid[name] != dhcid) && dhcid != "" {
		return ErrConflict
	}
	f.a[name] = ip.String()
	if dhcid != "" {
		f.dhcid[name] = dhcid
	}
	return nil
}

func (f *fakeDNS) RemoveForward(_ context.Context, _ *scope.DNSTarget, name string, ip netip.Addr, dhcid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("remove A %s %s", name, ip); err != nil {
		return err
	}
	if f.a[name] == ip.String() && (dhcid == "" || f.dhcid[name] == dhcid) {
		delete(f.a, name)
		delete(f.dhcid, name)
	}
	return nil
}

func (f *fakeDNS) AddReverse(_ context.Context, t *scope.DNSTarget, ip netip.Addr, name string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("add PTR %s %s @%s", ip, name, t.Zone); err != nil {
		return err
	}
	f.ptr[ip.String()] = name
	return nil
}

func (f *fakeDNS) RemoveReverse(_ context.Context, _ *scope.DNSTarget, ip netip.Addr, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("remove PTR %s %s", ip, name); err != nil {
		return err
	}
	if f.ptr[ip.String()] == name {
		delete(f.ptr, ip.String())
	}
	return nil
}

func (f *fakeDNS) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

type fakeLeases map[uint32]lease.Lease

func (f fakeLeases) Get(ip uint32) (lease.Lease, bool) { l, ok := f[ip]; return l, ok }

type env struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	scopes *scope.Store
	dns    *fakeDNS
	leases fakeLeases
	now    time.Time
	u      *Updater
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	s := scope.NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateDNSKey(ctx, scope.DNSKey{Name: "k1", Secret: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range []string{"lan.example", "0.10.in-addr.arpa"} {
		if _, err := s.CreateDNSZone(ctx, scope.DNSZone{Name: z, Server: "192.0.2.53", KeyID: k.ID}); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, ctx: ctx, db: db, scopes: s, dns: newFakeDNS(), leases: fakeLeases{}, now: time.Unix(1_800_000_000, 0)}
	e.u = e.newUpdater()
	return e
}

func (e *env) newUpdater() *Updater {
	u := NewUpdater(slog.New(slog.DiscardHandler), e.db, e.scopes, e.leases, e.dns)
	u.SetClock(func() time.Time { return e.now })
	if err := u.load(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// round runs one worker iteration.
func (e *env) round() {
	e.u.sweep(e.now)
	e.u.process(e.ctx)
	e.u.persist(e.ctx)
}

var settings = scope.DNSSettings{Mode: scope.DNSClient, Domain: "lan.example", Cleanup: true, Protect: true}

func (e *env) bind(ip, mac, name string) lease.Lease {
	v, _ := ip4.FromAddr(netip.MustParseAddr(ip))
	l := lease.Lease{IP: v, ScopeID: 1, MAC: mac, State: lease.StateActive, Start: e.now, Expires: e.now.Add(time.Hour)}
	e.leases[v] = l
	d := Decide(settings, wire(flagS|flagE, name), "", "")
	e.u.Bound(l, settings, d, time.Hour)
	return l
}

func (e *env) reg(ip string) *Registration {
	v, _ := ip4.FromAddr(netip.MustParseAddr(ip))
	for _, r := range e.u.Registrations() {
		if r.IP == v {
			return &r
		}
	}
	return nil
}

func (e *env) rows() int {
	var n int
	if err := e.db.R.QueryRow(`SELECT COUNT(*) FROM dns_registrations`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func expectCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
}

func TestUpdaterRegisterRenewRelease(t *testing.T) {
	e := newEnv(t)
	l := e.bind("10.0.0.5", "02:00:00:00:00:05", "pc5")
	e.round()
	expectCalls(t, e.dns.take(), "add A pc5.lan.example 10.0.0.5 @lan.example", "add PTR 10.0.0.5 pc5.lan.example @0.10.in-addr.arpa")
	if r := e.reg("10.0.0.5"); r == nil || r.State() != StateRegistered || r.HaveDHCID == "" {
		t.Fatalf("registration %+v", r)
	}
	if e.rows() != 1 {
		t.Fatal("registration not persisted")
	}

	// Renewal with the same name: nothing to do.
	e.bind("10.0.0.5", "02:00:00:00:00:05", "pc5")
	e.round()
	expectCalls(t, e.dns.take())

	// Release: the sweep removes the records and drops the registration.
	l.State = lease.StateReleased
	e.leases[l.IP] = l
	e.round()
	expectCalls(t, e.dns.take(), "remove A pc5.lan.example 10.0.0.5", "remove PTR 10.0.0.5 pc5.lan.example")
	if e.reg("10.0.0.5") != nil || e.rows() != 0 {
		t.Fatal("registration not dropped after removal")
	}
	if len(e.dns.a) != 0 || len(e.dns.ptr) != 0 {
		t.Fatalf("records left: %v %v", e.dns.a, e.dns.ptr)
	}
}

func TestUpdaterExpiryAndRename(t *testing.T) {
	e := newEnv(t)
	e.bind("10.0.0.6", "02:00:00:00:00:06", "old")
	e.round()
	e.dns.take()
	// The client renames itself: old A removed, new registered, PTR replaced.
	e.bind("10.0.0.6", "02:00:00:00:00:06", "new")
	e.round()
	expectCalls(t, e.dns.take(), "remove A old.lan.example 10.0.0.6", "add A new.lan.example 10.0.0.6 @lan.example", "add PTR 10.0.0.6 new.lan.example @0.10.in-addr.arpa")
	// Expiry is seen by the sweep.
	e.now = e.now.Add(2 * time.Hour)
	e.round()
	expectCalls(t, e.dns.take(), "remove A new.lan.example 10.0.0.6", "remove PTR 10.0.0.6 new.lan.example")
}

func TestUpdaterNameProtection(t *testing.T) {
	e := newEnv(t)
	e.bind("10.0.0.7", "02:00:00:00:00:07", "shared")
	e.round()
	e.dns.take()
	// Another client wants the same name: conflict, A stays with the owner,
	// PTR of the new address is still registered.
	e.bind("10.0.0.8", "02:00:00:00:00:08", "shared")
	e.round()
	expectCalls(t, e.dns.take(), "add A shared.lan.example 10.0.0.8 @lan.example", "add PTR 10.0.0.8 shared.lan.example @0.10.in-addr.arpa")
	r := e.reg("10.0.0.8")
	if r == nil || r.State() != StateConflict || r.HaveFwd != "" || r.LastError == "" {
		t.Fatalf("expected conflict, got %+v", r)
	}
	if e.dns.a["shared.lan.example"] != "10.0.0.7" {
		t.Fatal("protected name was overwritten")
	}
	var n int
	if err := e.db.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'dns.conflict'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("conflict audit entries: %d %v", n, err)
	}
	// No retry by itself ...
	e.round()
	expectCalls(t, e.dns.take())
	// ... but the next renewal tries again (the owner may be gone).
	e.bind("10.0.0.8", "02:00:00:00:00:08", "shared")
	e.round()
	expectCalls(t, e.dns.take(), "add A shared.lan.example 10.0.0.8 @lan.example")
}

func TestUpdaterRetryAndRestart(t *testing.T) {
	e := newEnv(t)
	e.dns.fail = errors.New("connection refused")
	l := e.bind("10.0.0.9", "02:00:00:00:00:09", "pc9")
	e.round()
	e.dns.take()
	r := e.reg("10.0.0.9")
	if r == nil || r.State() != StateError || r.Attempts != 1 || !r.NextAttempt.Equal(e.now.Add(30*time.Second)) {
		t.Fatalf("after failure: %+v", r)
	}
	e.round()
	expectCalls(t, e.dns.take()) // backoff
	e.dns.fail = nil
	e.now = e.now.Add(31 * time.Second)
	e.round()
	if r := e.reg("10.0.0.9"); r.State() != StateRegistered || r.LastError != "" {
		t.Fatalf("after retry: %+v", r)
	}
	e.dns.take()

	// Restart: the new updater knows the registration and cleans up after
	// the lease ended while it was down.
	l.State = lease.StateExpired
	e.leases[l.IP] = l
	e.u = e.newUpdater()
	e.round()
	expectCalls(t, e.dns.take(), "remove A pc9.lan.example 10.0.0.9", "remove PTR 10.0.0.9 pc9.lan.example")
	if e.rows() != 0 {
		t.Fatal("row not deleted")
	}
}

func TestUpdaterKeepRecordsAndModes(t *testing.T) {
	e := newEnv(t)
	keep := settings
	keep.Cleanup = false
	v, _ := ip4.FromAddr(netip.MustParseAddr("10.0.0.10"))
	l := lease.Lease{IP: v, ScopeID: 1, MAC: "02:00:00:00:00:0a", State: lease.StateActive, Expires: e.now.Add(time.Hour)}
	e.leases[v] = l
	e.u.Bound(l, keep, Decide(keep, wire(flagE, "pc10"), "", ""), time.Hour) // S=0: PTR only
	e.round()
	expectCalls(t, e.dns.take(), "add PTR 10.0.0.10 pc10.lan.example @0.10.in-addr.arpa")
	delete(e.leases, v)
	e.round()
	expectCalls(t, e.dns.take()) // records stay
	if e.reg("10.0.0.10") != nil || e.rows() != 0 {
		t.Fatal("registration not forgotten")
	}

	// A client without the option in "client" mode is not registered.
	e.u.Bound(l, settings, Decide(settings, nil, "pc10", ""), time.Hour)
	e.round()
	expectCalls(t, e.dns.take())

	// No zone for the name: error with retry, no DNS traffic.
	other := scope.DNSSettings{Mode: scope.DNSAlways, Domain: "elsewhere.example", Cleanup: true}
	e.leases[v] = l
	e.u.Bound(l, other, Decide(other, nil, "pc10", ""), time.Hour)
	e.round()
	r := e.reg("10.0.0.10")
	if r == nil || r.State() != StateError || r.HaveRev != "pc10.elsewhere.example" {
		t.Fatalf("no-zone registration: %+v", r)
	}
	expectCalls(t, e.dns.take(), "add PTR 10.0.0.10 pc10.elsewhere.example @0.10.in-addr.arpa")
}

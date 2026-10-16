package ha

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

type testNode struct {
	t      *testing.T
	cfg    config.HAConfig
	db     *store.DB
	scopes *scope.Store
	leases *lease.Manager
	node   *Node
	cancel context.CancelFunc
	done   chan error
}

func freePort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func haConfigs(t *testing.T, mode string) (a, b config.HAConfig) {
	t.Helper()
	base := config.Default().HA
	base.Mode = mode
	base.HeartbeatInterval = 100 * time.Millisecond
	base.CommunicationTimeout = 600 * time.Millisecond
	base.ReplicationTimeout = 500 * time.Millisecond
	base.MCLT = 2 * time.Second
	base.TLS = config.HATLSConfig{Mode: config.HATLSSecret, SharedSecret: "0123456789abcdef-test-secret"}
	addrA, addrB := freePort(t), freePort(t)
	a, b = base, base
	a.NodeID, a.Role, a.Listen, a.Peer = "dhcp-a", config.HAPrimary, addrA, config.HAPeerConfig{ID: "dhcp-b", Address: addrB}
	b.NodeID, b.Role, b.Listen, b.Peer = "dhcp-b", config.HASecondary, addrB, config.HAPeerConfig{ID: "dhcp-a", Address: addrA}
	return a, b
}

func quietLog() *slog.Logger {
	if os.Getenv("HA_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startNode(t *testing.T, cfg config.HAConfig, dir string) *testNode {
	t.Helper()
	ctx := context.Background()
	if dir == "" {
		dir = t.TempDir()
	}
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	log := quietLog().With("node", cfg.NodeID)
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	tn := &testNode{t: t, cfg: cfg, db: db, scopes: scope.NewStore(db)}
	if err := tn.scopes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	lc := lease.DefaultConfig()
	lc.NodeID = cfg.NodeID
	tn.leases = lease.NewManager(lc, log, lease.NewDBStore(db), nil)
	if err := tn.leases.Load(ctx); err != nil {
		t.Fatal(err)
	}
	tn.scopes.OnChange(tn.leases.ScopesChanged)
	tn.node, err = NewNode(ctx, Options{Config: cfg, Log: log, DB: db, Leases: tn.leases, Scopes: tn.scopes, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	tn.leases.SetCoordinator(tn.node)
	tn.scopes.OnChange(func(*scope.Snapshot) { tn.node.Kick() })
	rctx, cancel := context.WithCancel(ctx)
	tn.cancel = cancel
	tn.done = make(chan error, 2)
	go func() { tn.done <- tn.leases.Run(rctx) }()
	go func() { tn.done <- tn.node.Run(rctx) }()
	t.Cleanup(tn.stop)
	return tn
}

func (tn *testNode) stop() {
	if tn.cancel == nil {
		return
	}
	tn.cancel()
	for range 2 {
		if err := <-tn.done; err != nil {
			tn.t.Errorf("%s stopped with error: %v", tn.cfg.NodeID, err)
		}
	}
	tn.cancel = nil
	_ = tn.db.Close()
}

func (tn *testNode) state() State {
	tn.node.mu.Lock()
	defer tn.node.mu.Unlock()
	return tn.node.m.state
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func waitStates(t *testing.T, a, b *testNode, sa, sb State) {
	t.Helper()
	waitFor(t, fmt.Sprintf("states %s/%s", sa, sb), 10*time.Second, func() bool { return a.state() == sa && b.state() == sb })
}

func (tn *testNode) addScope(name, cidr, start, end string) *scope.Pool {
	tn.t.Helper()
	sc, err := tn.scopes.CreateScope(context.Background(), scope.Scope{
		Name: name, Subnet: netip.MustParsePrefix(cidr), Start: netip.MustParseAddr(start), End: netip.MustParseAddr(end),
		LeaseTime: time.Hour, Enabled: true,
	})
	if err != nil {
		tn.t.Fatal(err)
	}
	return tn.scopes.Snapshot().ByID(sc.ID)
}

func mac(i int) string { return fmt.Sprintf("02:00:00:%02x:%02x:%02x", i>>16&0xff, i>>8&0xff, i&0xff) }

// bind runs offer + request for client i on the node (as the DHCP handler
// would after Accept). It returns 0 if no address was available.
func (tn *testNode) bind(i int) uint32 {
	ctx := context.Background()
	c := lease.Client{MAC: mac(i)}
	p := tn.scopes.Snapshot().Pools()[0]
	o := tn.leases.Offer(ctx, c, []lease.Candidate{lease.PoolCandidate(p)}, 0)
	if o == nil {
		return 0
	}
	res, err := tn.leases.Request(ctx, c, lease.PoolCandidate(o.Pool), o.IP)
	if err != nil || !res.Ack {
		tn.t.Fatalf("request: %+v %v", res, err)
	}
	return res.Lease.IP
}

func activeByIP(tn *testNode) map[uint32]string {
	out := map[uint32]string{}
	for _, l := range tn.leases.All() {
		if l.State == lease.StateActive {
			out[l.IP] = l.MAC
		}
	}
	return out
}

func sameTables(a, b *testNode) bool {
	la, lb := a.leases.All(), b.leases.All()
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		x, y := la[i], lb[i]
		if x.IP != y.IP || x.MAC != y.MAC || x.State != y.State || !x.Expires.Equal(y.Expires) {
			return false
		}
	}
	return true
}

func TestPairConfigAndLeaseReplication(t *testing.T) {
	ca, cb := haConfigs(t, config.HALoadBalance)
	// Under load (race detector, parallel packages) synchronous updates can
	// time out and leases are shortened to MCLT (D-068); a longer MCLT keeps
	// them active until the counts below.
	ca.MCLT, cb.MCLT = time.Minute, time.Minute
	a, b := startNode(t, ca, ""), startNode(t, cb, "")
	waitStates(t, a, b, StateNormal, StateNormal)
	if a.node.ConfigReadOnly() || !b.node.ConfigReadOnly() {
		t.Fatal("primary is config leader, secondary read-only")
	}

	// Configuration made on the leader appears on the partner.
	a.addScope("lan", "10.1.0.0/16", "10.1.0.10", "10.1.3.250")
	waitFor(t, "scope on partner", 5*time.Second, func() bool { return len(b.scopes.Snapshot().Pools()) == 1 })
	if got := b.scopes.Snapshot().Pools()[0]; got.Name != "lan" || got.ID != a.scopes.Snapshot().Pools()[0].ID {
		t.Fatalf("replicated scope %+v", got.Scope)
	}
	waitStates(t, a, b, StateNormal, StateNormal)

	// Both servers bind their own clients concurrently.
	var wg sync.WaitGroup
	for i := range 600 {
		srv := a
		if ok, _ := a.node.Accept(mac(i), false); !ok {
			srv = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if srv.bind(i) == 0 {
				t.Errorf("client %d got no address", i)
			}
		}()
	}
	wg.Wait()
	waitFor(t, "identical lease tables", 20*time.Second, func() bool { return sameTables(a, b) })
	act := activeByIP(a)
	if len(act) != 600 {
		t.Fatalf("%d active leases, want 600", len(act))
	}
	seen := map[string]bool{}
	for _, m := range act {
		if seen[m] {
			t.Fatalf("client %s has two leases", m)
		}
		seen[m] = true
	}
	for _, l := range a.leases.All() {
		if want := map[bool]string{true: "dhcp-a", false: "dhcp-b"}[a.node.split.owns(l.IP)]; l.Origin != want {
			t.Fatalf("lease %s allocated by %s, owner is %s", ip4.String(l.IP), l.Origin, want)
		}
	}
}

func TestPairPartitionHealsWithoutDuplicates(t *testing.T) {
	ca, cb := haConfigs(t, config.HALoadBalance)
	a, b := startNode(t, ca, ""), startNode(t, cb, "")
	a.addScope("lan", "10.2.0.0/24", "10.2.0.10", "10.2.0.209")
	waitFor(t, "scope on partner", 5*time.Second, func() bool { return len(b.scopes.Snapshot().Pools()) == 1 })
	waitStates(t, a, b, StateNormal, StateNormal)
	for i := range 40 {
		srv := a
		if ok, _ := a.node.Accept(mac(i), false); !ok {
			srv = b
		}
		srv.bind(i)
	}

	a.node.blocked.Store(true)
	b.node.blocked.Store(true)
	waitStates(t, a, b, StateCommunicationInterrupted, StateCommunicationInterrupted)
	// Both serve every client now, but only from their own share and
	// with leases limited to MCLT.
	for i := 100; i < 160; i++ {
		ipA, ipB := a.bind(i), b.bind(i+1000)
		if ipA == 0 || ipB == 0 {
			t.Fatal("no address in COMMUNICATION_INTERRUPTED")
		}
		if !a.node.split.owns(ipA) || b.node.split.owns(ipA) || !b.node.split.owns(ipB) {
			t.Fatal("allocation outside the own share")
		}
	}
	if l, _ := a.leases.Get(a.bind(100)); l.Expires.After(time.Now().Add(3 * time.Second)) {
		t.Fatalf("lease in COMMUNICATION_INTERRUPTED not limited to MCLT: %v", l.Expires)
	}

	a.node.blocked.Store(false)
	b.node.blocked.Store(false)
	waitStates(t, a, b, StateNormal, StateNormal)
	waitFor(t, "identical lease tables", 5*time.Second, func() bool { return sameTables(a, b) })
	if n := len(activeByIP(a)); n != 40+120 {
		t.Fatalf("%d active leases after heal, want 160", n)
	}
}

func TestPairPartnerDownTakeover(t *testing.T) {
	ca, cb := haConfigs(t, config.HAHotStandby)
	cb.AutoPartnerDown = time.Second
	ca.AutoPartnerDown = time.Second
	dirA := t.TempDir()
	a, b := startNode(t, ca, dirA), startNode(t, cb, "")
	a.addScope("lan", "10.3.0.0/24", "10.3.0.10", "10.3.0.29")
	waitFor(t, "scope on partner", 5*time.Second, func() bool { return len(b.scopes.Snapshot().Pools()) == 1 })
	waitStates(t, a, b, StateNormal, StateNormal)
	if ok, _ := b.node.Accept(mac(1), false); ok {
		t.Fatal("standby must not answer in NORMAL")
	}
	first := a.bind(1)

	// The primary dies.
	a.stop()
	waitFor(t, "standby partner-down", 5*time.Second, func() bool { return b.state() == StatePartnerDown })
	if ok, _ := b.node.Accept(mac(1), false); !ok {
		t.Fatal("standby must answer after the primary failed")
	}
	// Existing client renews at the standby.
	res, err := b.leases.Request(context.Background(), lease.Client{MAC: mac(1)}, lease.PoolCandidate(b.scopes.Snapshot().Pools()[0]), first)
	if err != nil || !res.Ack {
		t.Fatalf("renewal at standby: %+v %v", res, err)
	}
	// Until MCLT has passed only the reserve is available...
	reserve := 0
	for ip := ip4.MustFromAddr(netip.MustParseAddr("10.3.0.10")); ip <= ip4.MustFromAddr(netip.MustParseAddr("10.3.0.29")); ip++ {
		if b.node.split.owns(ip) {
			reserve++
		}
	}
	got := 0
	for i := 2; i < 30 && b.bind(i) != 0; i++ {
		got++
	}
	if got != reserve {
		t.Fatalf("standby allocated %d addresses before MCLT, reserve is %d", got, reserve)
	}
	// ...afterwards the whole pool.
	time.Sleep(2 * time.Second)
	if b.bind(100) == 0 {
		t.Fatal("no takeover of the whole pool after MCLT")
	}

	// The primary comes back with its old database and resyncs.
	a2 := startNode(t, ca, dirA)
	waitStates(t, a2, b, StateNormal, StateNormal)
	waitFor(t, "identical lease tables", 5*time.Second, func() bool { return sameTables(a2, b) })
}

func TestPairWrongSecretRejected(t *testing.T) {
	ca, cb := haConfigs(t, config.HALoadBalance)
	cb.TLS.SharedSecret = "another-secret-0123456789"
	a, b := startNode(t, ca, ""), startNode(t, cb, "")
	time.Sleep(time.Second)
	if a.state() != StateRecover || b.state() != StateRecover {
		t.Fatalf("states %s/%s with wrong secret", a.state(), b.state())
	}
	if a.node.Status().Error == "" {
		t.Fatal("connection error not reported")
	}
}

func TestPairMutualTLS(t *testing.T) {
	dir := t.TempDir()
	bundle, err := GenerateCerts([]string{"dhcp-a", "dhcp-b", "intruder"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	ca, cb := haConfigs(t, config.HALoadBalance)
	tlsFor := func(id string) config.HATLSConfig {
		return config.HATLSConfig{Mode: config.HATLSMutual, CAFile: filepath.Join(dir, "ca.pem"),
			CertFile: filepath.Join(dir, id+".pem"), KeyFile: filepath.Join(dir, id+"-key.pem")}
	}
	ca.TLS, cb.TLS = tlsFor("dhcp-a"), tlsFor("dhcp-b")
	a, b := startNode(t, ca, ""), startNode(t, cb, "")
	waitStates(t, a, b, StateNormal, StateNormal)
	if a.node.Status().CertExpires.Before(time.Now().Add(4 * 365 * 24 * time.Hour)) {
		t.Fatal("certificate expiry not reported")
	}
	a.stop()
	b.stop()

	// A valid certificate of the same CA but for another node is refused.
	cb.TLS = tlsFor("intruder")
	if _, err := Credentials(cb); err == nil {
		t.Fatal("certificate for another node id accepted for dhcp-b")
	}
	ci := cb
	ci.NodeID, ci.TLS = "intruder", tlsFor("intruder")
	a2 := startNode(t, ca, "")
	i := startNode(t, ci, "")
	time.Sleep(time.Second)
	if a2.state() != StateRecover || i.state() != StateRecover {
		t.Fatalf("states %s/%s: impostor must not connect", a2.state(), i.state())
	}
}

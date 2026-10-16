//go:build integration && linux

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
	"github.com/meister119/leasorix/test/dhcpclient"
	"github.com/meister119/leasorix/test/netns"
)

// HA topology: two servers and the client namespace on one bridge (LAN
// 10.77.0.0/16), plus a dedicated link between the servers for the peer
// protocol (10.99.0.0/30) that the partition test cuts.
//
//	srvA a0 10.77.0.1 ─┐            ┌─ h0 10.99.0.1 (srvA)
//	srvB b0 10.77.0.2 ─┼─ br0 (sw)  │
//	cli  c0           ─┘            └─ h1 10.99.0.2 (srvB)

const itSetupToken = "it-setup-token-0123456789"

type haNode struct {
	t       *testing.T
	id      string
	role    string
	ns      *netns.NS
	ifname  string
	lanIP   string
	haIP    string
	peerID  string
	peerIP  string
	dir     string
	logPath string
	cmd     *exec.Cmd
	http    *http.Client
	csrf    string
}

type haCluster struct {
	t       *testing.T
	sw, cli *netns.NS
	a, b    *haNode
	mode    string
	extra   string
	certDir string
}

func newHACluster(t *testing.T, mode, extra string) *haCluster {
	t.Helper()
	sfx := netns.Suffix()
	var created []*netns.NS
	mk := func(name string) *netns.NS {
		ns, err := netns.New(name + sfx)
		if err != nil {
			for _, c := range created {
				_ = c.Delete()
			}
			t.Fatal(err)
		}
		created = append(created, ns)
		return ns
	}
	c := &haCluster{t: t, mode: mode, extra: extra, sw: mk("lxw"), cli: mk("lxc"), certDir: t.TempDir()}
	nsA, nsB := mk("lxa"), mk("lxb")
	c.a = &haNode{t: t, id: "dhcp-a", role: "primary", ns: nsA, ifname: "a0", lanIP: "10.77.0.1", haIP: "10.99.0.1", peerID: "dhcp-b", peerIP: "10.99.0.2", dir: t.TempDir()}
	c.b = &haNode{t: t, id: "dhcp-b", role: "secondary", ns: nsB, ifname: "b0", lanIP: "10.77.0.2", haIP: "10.99.0.2", peerID: "dhcp-a", peerIP: "10.99.0.1", dir: t.TempDir()}
	t.Cleanup(func() {
		for _, n := range []*haNode{c.a, c.b} {
			n.stop()
			if t.Failed() {
				n.dumpLog()
			}
		}
		for _, ns := range created {
			_ = ns.Delete()
		}
	})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.sw.Sh("ip link add br0 type bridge && ip link set br0 up"))
	must(netns.Veth(nsA, "a0", c.sw, "pa"))
	must(netns.Veth(nsB, "b0", c.sw, "pb"))
	must(netns.Veth(c.cli, "c0", c.sw, "pc"))
	must(c.sw.Sh("for p in pa pb pc; do ip link set $p master br0; done"))
	must(nsA.Sh("ip addr add 10.77.0.1/16 dev a0"))
	must(nsB.Sh("ip addr add 10.77.0.2/16 dev b0"))
	must(netns.Veth(nsA, "h0", nsB, "h1"))
	must(nsA.Sh("ip addr add 10.99.0.1/30 dev h0"))
	must(nsB.Sh("ip addr add 10.99.0.2/30 dev h1"))

	// Certificates via the CLI.
	out, err := exec.Command(binPath, "ha", "certs", "--out", c.certDir, "dhcp-a", "dhcp-b").CombinedOutput()
	if err != nil {
		t.Fatalf("ha certs: %v: %s", err, out)
	}
	return c
}

func (n *haNode) dbPath() string { return filepath.Join(n.dir, store.FileName) }

func (n *haNode) dumpLog() {
	b, err := os.ReadFile(n.logPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 80 {
		lines = lines[len(lines)-80:]
	}
	n.t.Logf("%s log (tail):\n%s", n.id, strings.Join(lines, "\n"))
}

// seed runs fn against the node's database before it starts.
func (n *haNode) seed(fn func(ctx context.Context, s *scope.Store)) {
	n.t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, n.dbPath())
	if err != nil {
		n.t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		n.t.Fatal(err)
	}
	s := scope.NewStore(db)
	if err := s.Load(ctx); err != nil {
		n.t.Fatal(err)
	}
	fn(ctx, s)
}

func (c *haCluster) start(n *haNode) {
	t := c.t
	t.Helper()
	cfg := fmt.Sprintf(`data_dir: %s
log: {level: debug, format: text}
http: {listen: "%s:8067", tls: {mode: "off"}}
auth: {setup_token: %q}
dhcp4:
  interfaces: [%s]
  expiry_interval: 1s
ha:
  mode: %s
  node_id: %s
  role: %s
  listen: "%s:8068"
  peer: {id: %s, address: "%s:8068"}
  heartbeat_interval: 500ms
  communication_timeout: 2s
  replication_timeout: 1s
  mclt: 1m
  tls:
    mode: mtls
    ca_file: %s
    cert_file: %s
    key_file: %s
%s`, n.dir, n.lanIP, itSetupToken, n.ifname, c.mode, n.id, n.role, n.haIP, n.peerID, n.peerIP,
		filepath.Join(c.certDir, "ca.pem"), filepath.Join(c.certDir, n.id+".pem"), filepath.Join(c.certDir, n.id+"-key.pem"), c.extra)
	cfgPath := filepath.Join(n.dir, "leasorix.yml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	n.logPath = filepath.Join(n.dir, "leasorixd.log")
	logf, err := os.OpenFile(n.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := n.ns.Command(context.Background(), binPath, "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = logf.Close()
	n.cmd = cmd
	jar, _ := cookiejar.New(nil)
	n.http = &http.Client{Timeout: 5 * time.Second, Jar: jar, Transport: &http.Transport{DialContext: n.ns.DialContext}}
	waitFor(t, 15*time.Second, n.id+" ready", func() bool {
		resp, err := n.http.Get("http://" + n.lanIP + ":8067/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

func (n *haNode) stop() {
	if n.cmd == nil {
		return
	}
	_ = n.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- n.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			n.t.Errorf("%s exited with %v", n.id, err)
		}
	case <-time.After(15 * time.Second):
		_ = n.cmd.Process.Kill()
		n.t.Errorf("%s did not stop on SIGTERM", n.id)
	}
	n.cmd = nil
}

// kill terminates the server hard (SIGKILL).
func (n *haNode) kill() {
	_ = n.cmd.Process.Kill()
	_ = n.cmd.Wait()
	n.cmd = nil
}

// state reads the HA state from the (unauthenticated) metrics endpoint.
func (n *haNode) state() string {
	resp, err := n.http.Get("http://" + n.lanIP + ":8067/metrics")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "leasorix_ha_state{") && strings.HasSuffix(line, " 1") {
			return line[len(`leasorix_ha_state{state="`):strings.Index(line, `"}`)]
		}
	}
	return ""
}

func (c *haCluster) waitStates(sa, sb string, timeout time.Duration) {
	c.t.Helper()
	waitFor(c.t, timeout, fmt.Sprintf("states %s/%s", sa, sb), func() bool { return c.a.state() == sa && c.b.state() == sb })
}

// api performs an authenticated API call (session cookie + CSRF).
func (n *haNode) api(method, path string, body any) (int, []byte) {
	n.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, "http://"+n.lanIP+":8067/api/v1"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if n.csrf != "" {
		req.Header.Set("X-CSRF-Token", n.csrf)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (n *haNode) session(code int, b []byte) bool {
	if code != http.StatusOK && code != http.StatusCreated {
		return false
	}
	var s struct {
		CSRF string `json:"csrf_token"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	n.csrf = s.CSRF
	return true
}

func (n *haNode) setupAdmin() {
	n.t.Helper()
	code, b := n.api("POST", "/setup", map[string]string{"setup_token": itSetupToken, "username": "admin", "password": "admin-password"})
	if !n.session(code, b) {
		n.t.Fatalf("setup on %s: %d %s", n.id, code, b)
	}
}

func (n *haNode) login() bool {
	code, b := n.api("POST", "/auth/login", map[string]string{"username": "admin", "password": "admin-password"})
	return n.session(code, b)
}

type dbLease struct {
	mac    string
	state  string
	origin string
	exp    int64
}

// leaseTable reads the persisted leases of a node (works while it runs).
func (n *haNode) leaseTable() map[string]dbLease {
	n.t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, n.dbPath())
	if err != nil {
		n.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.R.QueryContext(ctx, `SELECT ip, mac, state, origin, expires_at FROM leases`)
	if err != nil {
		n.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]dbLease{}
	for rows.Next() {
		var (
			ip int64
			l  dbLease
		)
		if err := rows.Scan(&ip, &l.mac, &l.state, &l.origin, &l.exp); err != nil {
			n.t.Fatal(err)
		}
		out[ip4.String(uint32(ip))] = l
	}
	return out
}

func activeOf(m map[string]dbLease) map[string]string {
	out := map[string]string{}
	for ip, l := range m {
		if l.state == "active" {
			out[ip] = l.mac
		}
	}
	return out
}

func sameActive(a, b map[string]dbLease) bool {
	x, y := activeOf(a), activeOf(b)
	if len(x) != len(y) {
		return false
	}
	for ip, m := range x {
		if y[ip] != m {
			return false
		}
	}
	return true
}

// checkUnique fails if an address is bound to two clients or a client holds
// two addresses.
func checkUnique(t *testing.T, active map[string]string) {
	t.Helper()
	byMAC := map[string]string{}
	for ip, mac := range active {
		if prev, ok := byMAC[mac]; ok {
			t.Fatalf("client %s holds %s and %s", mac, prev, ip)
		}
		byMAC[mac] = ip
	}
}

type result struct {
	ip, server, mac string
	leaseTime       time.Duration
}

// doraMany runs n clients concurrently and returns their results.
func doraMany(t *testing.T, conn *dhcpclient.Conn, n int) []result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		out  []result
		errs []error
	)
	sem := make(chan struct{}, 250)
	for range n {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			c := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
			ack, err := c.DORA(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.MAC, err))
				return
			}
			out = append(out, result{ip: ack.YourIPAddr.String(), server: ack.ServerIdentifier().String(), mac: c.MAC.String(), leaseTime: ack.IPAddressLeaseTime(0)})
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d of %d clients failed, first: %v", len(errs), n, errs[0])
	}
	seen := map[string]string{}
	for _, r := range out {
		if prev, dup := seen[r.ip]; dup {
			t.Fatalf("duplicate address %s for %s and %s", r.ip, prev, r.mac)
		}
		seen[r.ip] = r.mac
	}
	return out
}

func clientConn(t *testing.T, ns *netns.NS) *dhcpclient.Conn {
	t.Helper()
	var c *dhcpclient.Conn
	if err := ns.Do(func() error {
		var err error
		c, err = dhcpclient.Listen("c0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func seedScope(n *haNode, start, end string) {
	n.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(n.t, s, "lan", "10.77.0.0/16", start, end, time.Hour)
	})
}

// owner returns the node that owns a free address in load-balance mode
// with the default 50 % split.
func owner(ip string) string {
	if ha.AddressBucket(ip4.MustFromAddr(netip.MustParseAddr(ip))) < 50 {
		return "dhcp-a"
	}
	return "dhcp-b"
}

// TestHALoadBalance5000: both servers answer their half of 5000 clients,
// every client gets a unique address, both databases end up identical.
func TestHALoadBalance5000(t *testing.T) {
	c := newHACluster(t, "load-balance", "")
	seedScope(c.a, "10.77.1.0", "10.77.40.255")
	c.start(c.a)
	c.start(c.b)
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)

	conn := clientConn(t, c.cli)
	const n = 5000
	start := time.Now()
	res := doraMany(t, conn, n)
	elapsed := time.Since(start)
	served := map[string]int{}
	for _, r := range res {
		served[r.server]++
		if want := map[string]string{"dhcp-a": "10.77.0.1", "dhcp-b": "10.77.0.2"}[owner(r.ip)]; r.server != want {
			t.Fatalf("%s allocated %s from the partner's share", r.server, r.ip)
		}
	}
	if served["10.77.0.1"] < n/3 || served["10.77.0.2"] < n/3 {
		t.Fatalf("unbalanced: %v", served)
	}
	waitFor(t, 20*time.Second, "identical lease tables", func() bool {
		a, b := c.a.leaseTable(), c.b.leaseTable()
		return len(activeOf(a)) == n && sameActive(a, b)
	})
	checkUnique(t, activeOf(c.a.leaseTable()))
	t.Logf("%d clients in %s (%.0f leases/s), served by A/B: %d/%d", n, elapsed.Round(time.Millisecond),
		float64(n)/elapsed.Seconds(), served["10.77.0.1"], served["10.77.0.2"])
}

// TestHAHotStandbyFailover: the primary is killed, the standby keeps
// renewing all clients (leases limited to MCLT), takes over after
// auto partner-down and resynchronises with the returning primary.
func TestHAHotStandbyFailover(t *testing.T) {
	c := newHACluster(t, "hot-standby", "  auto_partner_down: 4s\n")
	seedScope(c.a, "10.77.1.0", "10.77.1.250")
	c.start(c.a)
	c.start(c.b)
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)

	conn := clientConn(t, c.cli)
	res := doraMany(t, conn, 40)
	for _, r := range res {
		if r.server != "10.77.0.1" {
			t.Fatalf("standby answered in NORMAL: %+v", r)
		}
	}
	// Synchronous replication: the standby has every lease as soon as the
	// clients have their ACK.
	if got := len(activeOf(c.b.leaseTable())); got != 40 {
		t.Fatalf("standby has %d leases right after the ACKs, want 40", got)
	}

	c.a.kill()
	waitFor(t, 10*time.Second, "standby in COMMUNICATION_INTERRUPTED", func() bool { return c.b.state() == "COMMUNICATION_INTERRUPTED" })

	// Replies to rebinding clients go unicast to ciaddr: configure the
	// addresses like real clients have them.
	for _, r := range res {
		if err := c.cli.Sh("ip addr add " + r.ip + "/32 dev c0"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, r := range res {
		mac, _ := netParseMAC(r.mac)
		cl := &dhcpclient.Client{Conn: conn, MAC: mac}
		ack, err := cl.Rebind(ctx, netParseIP(r.ip))
		if err != nil || ack.MessageType() != dhcpv4.MessageTypeAck {
			t.Fatalf("rebinding %s at the standby: %v %v", r.ip, ack, err)
		}
		if ack.ServerIdentifier().String() != "10.77.0.2" || ack.YourIPAddr.String() != r.ip {
			t.Fatalf("unexpected ACK %s", ack.Summary())
		}
		if lt := ack.IPAddressLeaseTime(0); lt > time.Minute {
			t.Fatalf("lease time %s exceeds MCLT while the partner is unreachable", lt)
		}
	}
	// A new client is served from the standby's reserve.
	if r := doraMany(t, conn, 1)[0]; r.server != "10.77.0.2" {
		t.Fatalf("new client: %+v", r)
	}
	waitFor(t, 10*time.Second, "standby in PARTNER_DOWN", func() bool { return c.b.state() == "PARTNER_DOWN" })

	// The primary returns with its old database and catches up.
	c.start(c.a)
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)
	waitFor(t, 10*time.Second, "identical lease tables", func() bool {
		a, b := c.a.leaseTable(), c.b.leaseTable()
		return len(activeOf(a)) == 41 && sameActive(a, b)
	})
	for ip, l := range c.a.leaseTable() {
		if bl := c.b.leaseTable()[ip]; l.exp != bl.exp {
			t.Fatalf("lease %s: expiry differs after resync (%d vs %d)", ip, l.exp, bl.exp)
		}
	}
}

// TestHAPartitionHeal: the peer link is cut; both servers keep serving
// from their own share; after healing the tables are merged without
// duplicate addresses.
func TestHAPartitionHeal(t *testing.T) {
	c := newHACluster(t, "load-balance", "")
	seedScope(c.a, "10.77.1.0", "10.77.4.255")
	c.start(c.a)
	c.start(c.b)
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)
	conn := clientConn(t, c.cli)
	before := doraMany(t, conn, 100)

	if err := c.a.ns.Sh("ip link set h0 down"); err != nil {
		t.Fatal(err)
	}
	c.waitStates("COMMUNICATION_INTERRUPTED", "COMMUNICATION_INTERRUPTED", 10*time.Second)
	during := doraMany(t, conn, 200)
	for _, r := range during {
		if r.leaseTime > time.Minute {
			t.Fatalf("lease time %s exceeds MCLT during the partition", r.leaseTime)
		}
	}
	for _, n := range []*haNode{c.a, c.b} {
		for ip, l := range n.leaseTable() {
			if l.state == "active" && l.origin != owner(ip) {
				t.Fatalf("%s: lease %s allocated by %s outside its share", n.id, ip, l.origin)
			}
		}
	}

	if err := c.a.ns.Sh("ip link set h0 up"); err != nil {
		t.Fatal(err)
	}
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)
	waitFor(t, 10*time.Second, "identical lease tables", func() bool {
		a, b := c.a.leaseTable(), c.b.leaseTable()
		return len(activeOf(a)) == len(before)+len(during) && sameActive(a, b)
	})
	checkUnique(t, activeOf(c.a.leaseTable()))
}

// TestHAConfigPropagation: configuration made on the leader is visible on
// the partner within seconds; the partner rejects configuration changes.
func TestHAConfigPropagation(t *testing.T) {
	c := newHACluster(t, "load-balance", "")
	c.start(c.a)
	c.start(c.b)
	c.waitStates("NORMAL", "NORMAL", 20*time.Second)
	c.a.setupAdmin()

	// The admin account is replicated: login on the partner works.
	waitFor(t, 10*time.Second, "admin login on the partner", c.b.login)

	start := time.Now()
	code, body := c.a.api("POST", "/scopes", map[string]any{"name": "lan", "subnet": "10.77.0.0/16", "start": "10.77.1.0", "end": "10.77.1.200", "lease_time": 3600, "enabled": true})
	if code != http.StatusCreated {
		t.Fatalf("create scope: %d %s", code, body)
	}
	waitFor(t, 10*time.Second, "scope on the partner", func() bool {
		code, b := c.b.api("GET", "/scopes", nil)
		return code == http.StatusOK && strings.Contains(string(b), `"10.77.1.200"`)
	})
	latency := time.Since(start)
	if latency > 5*time.Second {
		t.Fatalf("configuration change took %s to reach the partner", latency)
	}
	t.Logf("configuration change visible on the partner after %s", latency.Round(time.Millisecond))

	code, body = c.b.api("POST", "/scopes", map[string]any{"name": "x", "subnet": "10.78.0.0/16", "start": "10.78.1.0", "end": "10.78.1.200", "lease_time": 3600, "enabled": true})
	if code != http.StatusConflict || !strings.Contains(string(body), "dhcp-a") {
		t.Fatalf("partner accepted a configuration change: %d %s", code, body)
	}
	// The replicated scope serves clients on both servers.
	conn := clientConn(t, c.cli)
	served := map[string]bool{}
	for _, r := range doraMany(t, conn, 40) {
		served[r.server] = true
	}
	if len(served) != 2 {
		t.Fatalf("not both servers answered: %v", served)
	}
}

func netParseMAC(s string) (net.HardwareAddr, error) { return net.ParseMAC(s) }

func netParseIP(s string) net.IP { return net.ParseIP(s).To4() }

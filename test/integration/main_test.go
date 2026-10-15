//go:build integration && linux

// Package integration contains end-to-end tests that run leasorixd in Linux
// network namespaces against real DHCP clients (dhclient, udhcpc), a relay
// (dnsmasq --dhcp-relay) and the Go test client. Requires root.
//
//	make test-integration
package integration

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
	"github.com/meister119/leasorix/test/dhcpclient"
	"github.com/meister119/leasorix/test/netns"
)

var binPath string

func TestMain(m *testing.M) {
	if err := netns.Available(); err != nil {
		fmt.Println("skipping integration tests:", err)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "leasorix-it-")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "leasorixd")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/leasorixd")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Println("building leasorixd:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// topo is a server namespace and a client namespace connected by a veth
// pair: srv/s0 (10.77.0.1/16) <-> cli/c0.
type topo struct {
	t       *testing.T
	srv     *netns.NS
	cli     *netns.NS
	dir     string
	dbPath  string
	logPath string
	srvCmd  *exec.Cmd
	ifaces  []string
}

func newTopo(t *testing.T) *topo {
	t.Helper()
	sfx := netns.Suffix()
	srv, err := netns.New("lxs" + sfx)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := netns.New("lxc" + sfx)
	if err != nil {
		_ = srv.Delete()
		t.Fatal(err)
	}
	tp := &topo{t: t, srv: srv, cli: cli, dir: t.TempDir(), ifaces: []string{"s0"}}
	tp.dbPath = filepath.Join(tp.dir, store.FileName)
	tp.logPath = filepath.Join(tp.dir, "leasorixd.log")
	t.Cleanup(func() {
		tp.stopServer()
		if t.Failed() {
			if b, err := os.ReadFile(tp.logPath); err == nil {
				lines := strings.Split(string(b), "\n")
				if len(lines) > 60 {
					lines = lines[len(lines)-60:]
				}
				t.Logf("server log (tail):\n%s", strings.Join(lines, "\n"))
			}
		}
		_ = cli.Delete()
		_ = srv.Delete()
	})
	if err := netns.Veth(srv, "s0", cli, "c0"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Sh("ip addr add 10.77.0.1/16 dev s0"); err != nil {
		t.Fatal(err)
	}
	return tp
}

// seed runs fn against the database before the server starts.
func (tp *topo) seed(fn func(ctx context.Context, s *scope.Store)) {
	tp.t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, tp.dbPath)
	if err != nil {
		tp.t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		tp.t.Fatal(err)
	}
	s := scope.NewStore(db)
	if err := s.Load(ctx); err != nil {
		tp.t.Fatal(err)
	}
	fn(ctx, s)
}

func mustScope(t *testing.T, s *scope.Store, name, cidr, start, end string, lt time.Duration) scope.Scope {
	t.Helper()
	sc, err := s.CreateScope(context.Background(), scope.Scope{
		Name: name, Subnet: netip.MustParsePrefix(cidr), Start: netip.MustParseAddr(start),
		End: netip.MustParseAddr(end), LeaseTime: lt, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func (tp *topo) startServer(extra string) {
	tp.t.Helper()
	cfg := fmt.Sprintf(`data_dir: %s
log: {level: debug, format: text}
http: {listen: "10.77.0.1:8067"}
dhcp4:
  interfaces: [%s]
  expiry_interval: 1s
%s`, tp.dir, strings.Join(tp.ifaces, ", "), extra)
	cfgPath := filepath.Join(tp.dir, "leasorix.yml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		tp.t.Fatal(err)
	}
	logf, err := os.OpenFile(tp.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		tp.t.Fatal(err)
	}
	cmd := tp.srv.Command(context.Background(), binPath, "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		tp.t.Fatal(err)
	}
	_ = logf.Close()
	tp.srvCmd = cmd

	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: tp.srv.DialContext}}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://10.77.0.1:8067/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	tp.t.Fatal("server did not become ready")
}

func (tp *topo) stopServer() {
	if tp.srvCmd == nil {
		return
	}
	_ = tp.srvCmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- tp.srvCmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			tp.t.Errorf("server exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = tp.srvCmd.Process.Kill()
		tp.t.Error("server did not stop on SIGTERM")
	}
	tp.srvCmd = nil
}

// conn opens a Go test client socket on c0 inside the client namespace.
func (tp *topo) conn() *dhcpclient.Conn {
	tp.t.Helper()
	var c *dhcpclient.Conn
	err := tp.cli.Do(func() error {
		var err error
		c, err = dhcpclient.Listen("c0")
		return err
	})
	if err != nil {
		tp.t.Fatal(err)
	}
	tp.t.Cleanup(func() { _ = c.Close() })
	return c
}

type leaseRow struct {
	IP       string
	MAC      string
	ClientID string
	Hostname string
	State    string
	ScopeID  int64
	Expires  int64
}

// leases reads the persisted lease table (WAL allows reading while the
// server is running).
func (tp *topo) leases() []leaseRow {
	tp.t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, tp.dbPath)
	if err != nil {
		tp.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.R.QueryContext(ctx, `SELECT ip, mac, client_id, hostname, state, scope_id, expires_at FROM leases ORDER BY ip`)
	if err != nil {
		tp.t.Fatal(err)
	}
	defer rows.Close()
	var out []leaseRow
	for rows.Next() {
		var r leaseRow
		var ip int64
		if err := rows.Scan(&ip, &r.MAC, &r.ClientID, &r.Hostname, &r.State, &r.ScopeID, &r.Expires); err != nil {
			tp.t.Fatal(err)
		}
		r.IP = ip4.String(uint32(ip))
		out = append(out, r)
	}
	return out
}

func (tp *topo) leaseFor(ip string) (leaseRow, bool) {
	for _, l := range tp.leases() {
		if l.IP == ip {
			return l, true
		}
	}
	return leaseRow{}, false
}

// waitFor polls cond until it is true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

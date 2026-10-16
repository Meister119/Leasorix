//go:build linux

// Package netns provides helpers to build isolated test topologies with
// Linux network namespaces and veth pairs (requires root / CAP_NET_ADMIN and
// iproute2).
package netns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// Available reports whether namespaces can be used in this environment.
func Available() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return fmt.Errorf("iproute2 not installed: %w", err)
	}
	return nil
}

// NS is a named network namespace.
type NS struct {
	Name string
}

// Suffix returns a short random suffix to make names unique.
func Suffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// New creates a namespace with loopback up and reverse path filtering
// off. New namespaces inherit the IPv4 settings of the host, and hosts like
// Ubuntu enable rp_filter, which drops broadcast replies from the server on
// a client interface without an IPv4 route back.
func New(name string) (*NS, error) {
	if out, err := exec.CommandContext(context.Background(), "ip", "netns", "add", name).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ip netns add %s: %w: %s", name, err, out)
	}
	ns := &NS{Name: name}
	if err := ns.Sh("ip link set lo up && echo 0 > /proc/sys/net/ipv4/conf/all/rp_filter && echo 0 > /proc/sys/net/ipv4/conf/default/rp_filter"); err != nil {
		_ = ns.Delete()
		return nil, err
	}
	return ns, nil
}

// Delete removes the namespace (and the veth ends inside it).
func (n *NS) Delete() error {
	out, err := exec.CommandContext(context.Background(), "ip", "netns", "del", n.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip netns del %s: %w: %s", n.Name, err, out)
	}
	return nil
}

// Command returns a command running inside the namespace.
func (n *NS) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "ip", append([]string{"netns", "exec", n.Name, name}, args...)...)
}

// Sh runs a shell snippet inside the namespace.
func (n *NS) Sh(script string) error {
	out, err := exec.CommandContext(context.Background(), "ip", "netns", "exec", n.Name, "sh", "-ec", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("[%s] %s: %w: %s", n.Name, script, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Output runs a command inside the namespace and returns its output.
func (n *NS) Output(name string, args ...string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ip", append([]string{"netns", "exec", n.Name, name}, args...)...).CombinedOutput()
	return string(out), err
}

// Veth creates a veth pair with one end in a and one in b, both up.
func Veth(a *NS, aName string, b *NS, bName string) error {
	tmpA, tmpB := "vt"+Suffix(), "vt"+Suffix()
	steps := [][]string{
		{"link", "add", tmpA, "type", "veth", "peer", "name", tmpB},
		{"link", "set", tmpA, "netns", a.Name},
		{"link", "set", tmpB, "netns", b.Name},
	}
	for _, s := range steps {
		if out, err := exec.CommandContext(context.Background(), "ip", s...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %v: %w: %s", s, err, out)
		}
	}
	if err := a.Sh(fmt.Sprintf("ip link set %s name %s && ip link set %s up", tmpA, aName, aName)); err != nil {
		return err
	}
	return b.Sh(fmt.Sprintf("ip link set %s name %s && ip link set %s up", tmpB, bName, bName))
}

// Do runs fn on an OS thread that has entered the namespace. Sockets created
// inside fn belong to the namespace and can be used from any goroutine
// afterwards. The thread is discarded when fn returns.
func (n *NS) Do(fn func() error) error {
	errc := make(chan error, 1)
	go func() {
		// Never unlock: the thread is in a foreign namespace and must die
		// with this goroutine.
		runtime.LockOSThread()
		fd, err := unix.Open("/var/run/netns/"+n.Name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			errc <- err
			return
		}
		defer unix.Close(fd)
		if err := unix.Setns(fd, unix.CLONE_NEWNET); err != nil {
			errc <- fmt.Errorf("setns %s: %w", n.Name, err)
			return
		}
		errc <- fn()
	}()
	return <-errc
}

// DialContext dials from inside the namespace (for http.Transport).
func (n *NS) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var conn net.Conn
	err := n.Do(func() error {
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		conn = c
		return err
	})
	return conn, err
}

//go:build linux

package dhcp6

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"golang.org/x/net/ipv6"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/meister119/leasorix/internal/metrics"
)

// AllServers is the All_DHCP_Relay_Agents_and_Servers multicast group.
var AllServers = netip.MustParseAddr("ff02::1:2")

// Server owns one UDP socket per interface, bound to [::]:547, pinned to
// the interface and joined to ff02::1:2 on it.
type Server struct {
	log     *slog.Logger
	handler *Handler
	ifaces  []string
	workers int
	port    int

	mu    sync.Mutex
	bound map[string]bool

	metrics *metrics.DHCP6
}

// NewServer creates a server for the given interfaces.
func NewServer(log *slog.Logger, handler *Handler, ifaces []string, workers int) *Server {
	if workers < 1 {
		workers = 1
	}
	return &Server{log: log, handler: handler, ifaces: ifaces, workers: workers, port: ServerPort, bound: map[string]bool{}}
}

// SetMetrics enables transport metrics.
func (s *Server) SetMetrics(m *metrics.DHCP6) { s.metrics = m }

// Ready reports an error until all interfaces are bound.
func (s *Server) Ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var missing []string
	for _, n := range s.ifaces {
		if !s.bound[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not listening on %v", missing)
	}
	return nil
}

func (s *Server) setBound(name string, v bool) {
	s.mu.Lock()
	s.bound[name] = v
	s.mu.Unlock()
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	sem := make(chan struct{}, s.workers)
	var wg sync.WaitGroup
	g, gctx := errgroup.WithContext(ctx)
	for _, name := range s.ifaces {
		g.Go(func() error { return s.serveInterface(gctx, name, sem, &wg) })
	}
	err := g.Wait()
	wg.Wait()
	return err
}

func (s *Server) serveInterface(ctx context.Context, name string, sem chan struct{}, wg *sync.WaitGroup) error {
	log := s.log.With("if", name)
	for {
		ic, err := s.open(ctx, name)
		if err != nil {
			log.Warn("cannot listen on interface, retrying", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
				continue
			}
		}
		s.setBound(name, true)
		log.Info("dhcpv6 listening", "ifindex", ic.ifi.Index, "addrs", ic.addresses())
		rerr := s.readLoop(ctx, ic, sem, wg)
		s.setBound(name, false)
		_ = ic.pc.Close()
		if rerr != nil {
			log.Error("socket failed, reopening", "err", rerr)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

type ifaceConn struct {
	ifi *net.Interface
	pc  *ipv6.PacketConn

	mu      sync.Mutex
	addrs   []netip.Prefix
	addrsAt time.Time
}

func (s *Server) open(ctx context.Context, name string) (*ifaceConn, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
				return
			}
			serr = unix.BindToDevice(int(fd), name)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	conn, err := lc.ListenPacket(ctx, "udp6", fmt.Sprintf("[::]:%d", s.port))
	if err != nil {
		return nil, err
	}
	pc := ipv6.NewPacketConn(conn)
	if err := pc.JoinGroup(ifi, &net.UDPAddr{IP: AllServers.AsSlice()}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("joining %s: %w", AllServers, err)
	}
	if err := pc.SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &ifaceConn{ifi: ifi, pc: pc}, nil
}

// addresses returns the global and unique local prefixes of the
// interface, cached for a few seconds.
func (ic *ifaceConn) addresses() []netip.Prefix {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if time.Since(ic.addrsAt) < 10*time.Second && ic.addrs != nil {
		return ic.addrs
	}
	as, err := ic.ifi.Addrs()
	if err != nil {
		return ic.addrs
	}
	out := []netip.Prefix{}
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() != nil {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP.To16())
		if !ok || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
			continue
		}
		bits, _ := n.Mask.Size()
		out = append(out, netip.PrefixFrom(ip, bits))
	}
	ic.addrs, ic.addrsAt = out, time.Now()
	return out
}

func (s *Server) readLoop(ctx context.Context, ic *ifaceConn, sem chan struct{}, wg *sync.WaitGroup) error {
	stop := context.AfterFunc(ctx, func() { _ = ic.pc.Close() })
	defer stop()
	buf := make([]byte, 65536)
	for {
		n, cm, src, err := ic.pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.handlePacket(ctx, ic, pkt, cm, src)
		}()
	}
}

func (s *Server) handlePacket(ctx context.Context, ic *ifaceConn, pkt []byte, cm *ipv6.ControlMessage, src net.Addr) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic while handling packet", "panic", r, "if", ic.ifi.Name)
		}
	}()
	msg, err := dhcpv6.FromBytes(pkt)
	if err != nil {
		s.log.Debug("dropping malformed packet", "if", ic.ifi.Name, "err", err)
		s.metrics.Dropped("malformed")
		return
	}
	req := &Request{Msg: msg, IfName: ic.ifi.Name, IfAddrs: ic.addresses()}
	if ua, ok := src.(*net.UDPAddr); ok {
		a, _ := netip.AddrFromSlice(ua.IP.To16())
		req.Src = netip.AddrPortFrom(a, uint16(ua.Port))
	}
	if cm != nil {
		req.Dst, _ = netip.AddrFromSlice(cm.Dst.To16())
	}
	resp := s.handler.Handle(ctx, req)
	if resp == nil {
		return
	}
	dst := &net.UDPAddr{IP: resp.Dst.Addr().AsSlice(), Port: int(resp.Dst.Port())}
	if resp.Dst.Addr().IsLinkLocalUnicast() {
		dst.Zone = ic.ifi.Name
	}
	if _, err := ic.pc.WriteTo(resp.Msg.ToBytes(), nil, dst); err != nil {
		s.log.Warn("sending reply failed", "if", ic.ifi.Name, "dst", dst.String(), "err", err)
		s.metrics.Dropped("send_error")
	}
}

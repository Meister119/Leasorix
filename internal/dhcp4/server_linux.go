//go:build linux

package dhcp4

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
	"golang.org/x/net/ipv4"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/meister119/leasorix/internal/metrics"
)

// Server owns one UDP socket per configured interface. Each socket is bound
// to 0.0.0.0:67 and pinned to its interface with SO_BINDTODEVICE so that
// broadcasts are received and answered on the right link.
type Server struct {
	log     *slog.Logger
	handler *Handler
	ifaces  []string
	workers int
	port    int

	mu    sync.Mutex
	bound map[string]bool

	raw     *rawSender
	metrics *metrics.DHCP4
}

// SetMetrics enables transport metrics.
func (s *Server) SetMetrics(m *metrics.DHCP4) { s.metrics = m }

// NewServer creates a server for the given interfaces. workers bounds the
// number of packets processed concurrently.
func NewServer(log *slog.Logger, handler *Handler, ifaces []string, workers int) *Server {
	if workers < 1 {
		workers = 1
	}
	return &Server{log: log, handler: handler, ifaces: ifaces, workers: workers, port: ServerPort, bound: map[string]bool{}}
}

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
	if raw, err := newRawSender(); err != nil {
		s.log.Warn("raw packet socket unavailable; replies to clients without address are broadcast", "err", err)
	} else {
		s.raw = raw
		defer raw.close()
	}
	sem := make(chan struct{}, s.workers)
	var wg sync.WaitGroup
	g, gctx := errgroup.WithContext(ctx)
	for _, name := range s.ifaces {
		g.Go(func() error { return s.serveInterface(gctx, name, sem, &wg) })
	}
	err := g.Wait()
	wg.Wait() // let in-flight handlers finish
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
		log.Info("dhcpv4 listening", "ifindex", ic.index, "addrs", ic.addresses())
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
	name  string
	index int
	pc    *ipv4.PacketConn

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
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1); serr != nil {
				return
			}
			serr = unix.BindToDevice(int(fd), name)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	conn, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", s.port))
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(conn)
	if err := pc.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &ifaceConn{name: name, index: ifi.Index, pc: pc}, nil
}

// addresses returns the interface's IPv4 addresses, cached for a few
// seconds.
func (ic *ifaceConn) addresses() []netip.Prefix {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if time.Since(ic.addrsAt) < 10*time.Second && ic.addrs != nil {
		return ic.addrs
	}
	ifi, err := net.InterfaceByName(ic.name)
	if err != nil {
		return ic.addrs
	}
	as, err := ifi.Addrs()
	if err != nil {
		return ic.addrs
	}
	var out []netip.Prefix
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil {
			continue
		}
		ip, _ := netip.AddrFromSlice(n.IP.To4())
		bits, _ := n.Mask.Size()
		out = append(out, netip.PrefixFrom(ip, bits))
	}
	ic.addrs, ic.addrsAt = out, time.Now()
	return out
}

func (s *Server) readLoop(ctx context.Context, ic *ifaceConn, sem chan struct{}, wg *sync.WaitGroup) error {
	stop := context.AfterFunc(ctx, func() { _ = ic.pc.Close() })
	defer stop()
	buf := make([]byte, 4096)
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

func (s *Server) handlePacket(ctx context.Context, ic *ifaceConn, pkt []byte, cm *ipv4.ControlMessage, src net.Addr) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic while handling packet", "panic", r, "if", ic.name)
		}
	}()
	msg, err := dhcpv4.FromBytes(pkt)
	if err != nil {
		s.log.Debug("dropping malformed packet", "if", ic.name, "err", err)
		s.metrics.Dropped("malformed")
		return
	}
	req := &Request{Msg: msg, IfName: ic.name, IfAddrs: ic.addresses()}
	if ua, ok := src.(*net.UDPAddr); ok {
		req.Src = ua.AddrPort()
	}
	if cm != nil {
		req.Dst, _ = netip.AddrFromSlice(cm.Dst.To4())
	}
	resp := s.handler.Handle(ctx, req)
	if resp == nil {
		return
	}
	if err := s.send(ic, resp); err != nil {
		s.log.Warn("sending reply failed", "if", ic.name, "dst", resp.Dst.String(), "err", err)
		s.metrics.Error("send")
		return
	}
	s.metrics.Sent(resp.Msg.MessageType().String())
}

func (s *Server) send(ic *ifaceConn, resp *Response) error {
	b := resp.Msg.ToBytes()
	dst := resp.Dst
	m := resp.Msg
	if resp.L2Unicast {
		if s.raw != nil && m.HWType == iana.HWTypeEthernet && len(m.ClientHWAddr) == 6 && resp.Src.IsValid() {
			err := s.raw.send(ic.index, m.ClientHWAddr, resp.Src, dst.Addr(), ServerPort, ClientPort, b)
			if err == nil {
				return nil
			}
			s.log.Debug("raw unicast failed, falling back to broadcast", "err", err)
		}
		dst = netip.AddrPortFrom(bcastAddr, ClientPort)
	}
	var cm *ipv4.ControlMessage
	for _, a := range ic.addresses() {
		if a.Addr() == resp.Src {
			cm = &ipv4.ControlMessage{Src: resp.Src.AsSlice()}
			break
		}
	}
	_, err := ic.pc.WriteTo(b, cm, net.UDPAddrFromAddrPort(dst))
	return err
}

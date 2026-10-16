package lease

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/macaddr"
)

// ICMPProber implements conflict detection with ICMP echo requests
// (equivalent to the "conflict detection attempts" of Windows DHCP).
//
// If an address answers, the kernel ARP cache is consulted: an answer from
// the requesting client itself (e.g. it still has its old address
// configured) is not a conflict.
type ICMPProber struct {
	log      *slog.Logger
	timeout  time.Duration
	attempts int

	conn       *icmp.PacketConn
	privileged bool
	id         int
	seq        atomic.Uint32

	mu      sync.Mutex
	waiters map[uint16]*probeWaiter

	arpPath string
}

type probeWaiter struct {
	ip uint32
	ch chan struct{}
}

// NewICMPProber opens an ICMP socket. It prefers a raw socket (needs
// CAP_NET_RAW) and falls back to an unprivileged ICMP datagram socket.
func NewICMPProber(log *slog.Logger, timeout time.Duration, attempts int) (*ICMPProber, error) {
	if attempts < 1 {
		attempts = 1
	}
	p := &ICMPProber{
		log:      log,
		timeout:  timeout,
		attempts: attempts,
		id:       os.Getpid() & 0xffff,
		waiters:  map[uint16]*probeWaiter{},
		arpPath:  "/proc/net/arp",
	}
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err == nil {
		p.conn, p.privileged = conn, true
		return p, nil
	}
	conn, err2 := icmp.ListenPacket("udp4", "0.0.0.0")
	if err2 != nil {
		return nil, fmt.Errorf("open ICMP socket: raw: %w; unprivileged: %w", err, err2)
	}
	p.conn = conn
	return p, nil
}

// Run reads echo replies until ctx is cancelled.
func (p *ICMPProber) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = p.conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, peer, err := p.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // socket closed on shutdown
			}
			p.log.Debug("icmp read", "err", err)
			continue
		}
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || (p.privileged && echo.ID != p.id) {
			continue
		}
		src := peerIP(peer)
		p.mu.Lock()
		w := p.waiters[uint16(echo.Seq)]
		if w != nil && w.ip == src {
			delete(p.waiters, uint16(echo.Seq))
			close(w.ch)
		}
		p.mu.Unlock()
	}
}

func peerIP(a net.Addr) uint32 {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	}
	u, _ := ip4.FromIP(ip)
	return u
}

// InUse implements Prober.
func (p *ICMPProber) InUse(ctx context.Context, ip uint32, clientMAC string) bool {
	for range p.attempts {
		if p.ping(ctx, ip) {
			if mac := p.arpLookup(ip); mac != "" && mac == clientMAC {
				p.log.Debug("probe answered by the requesting client itself", "ip", ip4.String(ip), "mac", mac)
				return false
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

func (p *ICMPProber) ping(ctx context.Context, ip uint32) bool {
	seq := uint16(p.seq.Add(1))
	w := &probeWaiter{ip: ip, ch: make(chan struct{})}
	p.mu.Lock()
	p.waiters[seq] = w
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.waiters[seq] == w {
			delete(p.waiters, seq)
		}
		p.mu.Unlock()
	}()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: p.id, Seq: int(seq), Data: []byte("leasorix-conflict-check")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return false
	}
	var dst net.Addr = &net.IPAddr{IP: ip4.ToIP(ip)}
	if !p.privileged {
		dst = &net.UDPAddr{IP: ip4.ToIP(ip)}
	}
	if _, err := p.conn.WriteTo(b, dst); err != nil {
		p.log.Debug("icmp send", "ip", ip4.String(ip), "err", err)
		return false
	}
	t := time.NewTimer(p.timeout)
	defer t.Stop()
	select {
	case <-w.ch:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// arpLookup returns the MAC of ip from the kernel ARP cache, or "".
func (p *ICMPProber) arpLookup(ip uint32) string {
	f, err := os.Open(p.arpPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	return parseARP(bufio.NewScanner(f), ip4.String(ip))
}

func parseARP(sc *bufio.Scanner, ip string) string {
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// IP address, HW type, Flags, HW address, Mask, Device
		if len(f) >= 4 && f[0] == ip && f[2] != "0x0" {
			if mac, err := macaddr.Normalize(f[3]); err == nil {
				return mac
			}
		}
	}
	return ""
}

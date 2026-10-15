//go:build linux

// Package dhcpclient is a DHCPv4 client simulator for integration and load
// tests. One Conn (a UDP socket on port 68 bound to an interface) can carry
// thousands of simulated clients; replies are dispatched by transaction id.
// Simulated clients set the broadcast flag because their (random) MAC and
// address are not configured on the interface.
package dhcpclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"golang.org/x/sys/unix"
)

// Conn is a shared client socket.
type Conn struct {
	pc net.PacketConn

	mu      sync.Mutex
	waiters map[dhcpv4.TransactionID]chan *dhcpv4.DHCPv4
	closed  chan struct{}
}

// Listen opens 0.0.0.0:68 bound to ifname. Call it inside the target
// namespace (see netns.NS.Do).
func Listen(ifname string) (*Conn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
				return
			}
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1); serr != nil {
				return
			}
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20); serr != nil {
				return
			}
			serr = unix.BindToDevice(int(fd), ifname)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:68")
	if err != nil {
		return nil, err
	}
	c := &Conn{pc: pc, waiters: map[dhcpv4.TransactionID]chan *dhcpv4.DHCPv4{}, closed: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Close closes the socket.
func (c *Conn) Close() error { return c.pc.Close() }

func (c *Conn) readLoop() {
	defer close(c.closed)
	buf := make([]byte, 4096)
	for {
		n, _, err := c.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		m, err := dhcpv4.FromBytes(buf[:n])
		if err != nil || m.OpCode != dhcpv4.OpcodeBootReply {
			continue
		}
		c.mu.Lock()
		ch := c.waiters[m.TransactionID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

// ErrTimeout is returned when no matching reply arrives.
var ErrTimeout = errors.New("no reply")

var bcast = &net.UDPAddr{IP: net.IPv4bcast, Port: 67}

// Exchange broadcasts msg (retransmitting) and waits for a reply of one of
// the accepted types. With want empty no reply is awaited.
func (c *Conn) Exchange(ctx context.Context, msg *dhcpv4.DHCPv4, want ...dhcpv4.MessageType) (*dhcpv4.DHCPv4, error) {
	ch := make(chan *dhcpv4.DHCPv4, 4)
	c.mu.Lock()
	c.waiters[msg.TransactionID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, msg.TransactionID)
		c.mu.Unlock()
	}()
	b := msg.ToBytes()
	if len(want) == 0 {
		_, err := c.pc.WriteTo(b, bcast)
		return nil, err
	}
	interval := 2 * time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := c.pc.WriteTo(b, bcast); err != nil {
			return nil, err
		}
		t := time.NewTimer(interval)
	wait:
		for {
			select {
			case m := <-ch:
				for _, w := range want {
					if m.MessageType() == w {
						t.Stop()
						return m, nil
					}
				}
			case <-t.C:
				break wait
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-c.closed:
				t.Stop()
				return nil, net.ErrClosed
			}
		}
	}
	return nil, ErrTimeout
}

// Client is one simulated DHCP client.
type Client struct {
	Conn     *Conn
	MAC      net.HardwareAddr
	ClientID []byte
	Hostname string
	// Extra options added to every message.
	Options []dhcpv4.Option
}

// RandomMAC returns a locally administered unicast MAC.
func RandomMAC() net.HardwareAddr {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	b[0] = (b[0] | 0x02) &^ 0x01
	return b
}

func (cl *Client) base(mt dhcpv4.MessageType, mods ...dhcpv4.Modifier) (*dhcpv4.DHCPv4, error) {
	all := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithHwAddr(cl.MAC),
		dhcpv4.WithBroadcast(true),
	}
	if len(cl.ClientID) > 0 {
		all = append(all, dhcpv4.WithOption(dhcpv4.OptClientIdentifier(cl.ClientID)))
	}
	if cl.Hostname != "" {
		all = append(all, dhcpv4.WithOption(dhcpv4.OptHostName(cl.Hostname)))
	}
	for _, o := range cl.Options {
		all = append(all, dhcpv4.WithOption(o))
	}
	return dhcpv4.New(append(all, mods...)...)
}

// Discover sends DHCPDISCOVER and returns the OFFER.
func (cl *Client) Discover(ctx context.Context, mods ...dhcpv4.Modifier) (*dhcpv4.DHCPv4, error) {
	m, err := cl.base(dhcpv4.MessageTypeDiscover, mods...)
	if err != nil {
		return nil, err
	}
	return cl.Conn.Exchange(ctx, m, dhcpv4.MessageTypeOffer)
}

// Request sends a SELECTING DHCPREQUEST for the offer and returns ACK/NAK.
func (cl *Client) Request(ctx context.Context, offer *dhcpv4.DHCPv4) (*dhcpv4.DHCPv4, error) {
	m, err := cl.base(dhcpv4.MessageTypeRequest,
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(offer.ServerIdentifier())),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offer.YourIPAddr)),
	)
	if err != nil {
		return nil, err
	}
	return cl.Conn.Exchange(ctx, m, dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak)
}

// RequestAddress sends an INIT-REBOOT style DHCPREQUEST for ip.
func (cl *Client) RequestAddress(ctx context.Context, ip net.IP) (*dhcpv4.DHCPv4, error) {
	m, err := cl.base(dhcpv4.MessageTypeRequest, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ip)))
	if err != nil {
		return nil, err
	}
	return cl.Conn.Exchange(ctx, m, dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak)
}

// DORA runs DISCOVER/OFFER/REQUEST/ACK and returns the ACK.
func (cl *Client) DORA(ctx context.Context) (*dhcpv4.DHCPv4, error) {
	offer, err := cl.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	ack, err := cl.Request(ctx, offer)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if ack.MessageType() != dhcpv4.MessageTypeAck {
		return ack, fmt.Errorf("got %s: %s", ack.MessageType(), ack.Message())
	}
	return ack, nil
}

// Release sends DHCPRELEASE for the acknowledged address (broadcast, since
// the simulated client has no address configured).
func (cl *Client) Release(ctx context.Context, ack *dhcpv4.DHCPv4) error {
	m, err := cl.base(dhcpv4.MessageTypeRelease,
		dhcpv4.WithBroadcast(false),
		dhcpv4.WithClientIP(ack.YourIPAddr),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(ack.ServerIdentifier())),
	)
	if err != nil {
		return err
	}
	_, err = cl.Conn.Exchange(ctx, m)
	return err
}

// Decline sends DHCPDECLINE for the acknowledged address.
func (cl *Client) Decline(ctx context.Context, ack *dhcpv4.DHCPv4) error {
	m, err := cl.base(dhcpv4.MessageTypeDecline,
		dhcpv4.WithBroadcast(false),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(ack.ServerIdentifier())),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ack.YourIPAddr)),
	)
	if err != nil {
		return err
	}
	_, err = cl.Conn.Exchange(ctx, m)
	return err
}

// Inform sends DHCPINFORM for a statically configured address.
func (cl *Client) Inform(ctx context.Context, ip net.IP) (*dhcpv4.DHCPv4, error) {
	m, err := cl.base(dhcpv4.MessageTypeInform, dhcpv4.WithClientIP(ip))
	if err != nil {
		return nil, err
	}
	return cl.Conn.Exchange(ctx, m, dhcpv4.MessageTypeAck)
}

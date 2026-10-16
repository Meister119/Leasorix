package dnsupdate

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/meister119/leasorix/internal/scope"
)

// ErrConflict means the name belongs to another client or was created
// manually (name protection, RFC 4703).
var ErrConflict = errors.New("name is in use by another client")

// errPrereq is a failed prerequisite (YXDOMAIN, NXRRSET, ...).
type errPrereq struct{ rcode int }

func (e errPrereq) Error() string { return "prerequisite failed: " + dns.RcodeToString[e.rcode] }

// tsigAlgorithms maps the configured names to the TSIG algorithm names.
var tsigAlgorithms = map[string]string{
	"hmac-sha256": dns.HmacSHA256,
	"hmac-sha384": dns.HmacSHA384,
	"hmac-sha512": dns.HmacSHA512,
	"hmac-sha224": dns.HmacSHA224,
	"hmac-sha1":   dns.HmacSHA1,
	"hmac-md5":    dns.HmacMD5,
}

// Client sends RFC 2136 updates.
type Client struct {
	// Timeout per exchange.
	Timeout time.Duration
	// now is the TSIG time source (tests).
	now func() time.Time
}

// NewClient returns a client with a 5 second timeout.
func NewClient() *Client { return &Client{Timeout: 5 * time.Second, now: time.Now} }

func fqdn(name string) string { return dns.CanonicalName(strings.TrimSuffix(name, ".") + ".") }

func hdr(name string, rrtype uint16, ttl uint32) dns.RR_Header {
	return dns.RR_Header{Name: fqdn(name), Rrtype: rrtype, Class: dns.ClassINET, Ttl: ttl}
}

func aRR(name string, ip netip.Addr, ttl uint32) dns.RR {
	return &dns.A{Hdr: hdr(name, dns.TypeA, ttl), A: ip.AsSlice()}
}

func dhcidRR(name, dhcid string, ttl uint32) dns.RR {
	return &dns.DHCID{Hdr: hdr(name, dns.TypeDHCID, ttl), Digest: dhcid}
}

func ptrRR(ip netip.Addr, name string, ttl uint32) dns.RR {
	rev, _ := dns.ReverseAddr(ip.String())
	return &dns.PTR{Hdr: hdr(rev, dns.TypePTR, ttl), Ptr: fqdn(name)}
}

// rrset returns an RR that names an RRset (for RRset prerequisites and
// deletions).
func rrset(name string, rrtype uint16) dns.RR {
	return &dns.ANY{Hdr: dns.RR_Header{Name: fqdn(name), Rrtype: rrtype, Class: dns.ClassANY}}
}

// exchange sends an update to the zone's server, signed when a key is
// configured, and maps the response code.
func (c *Client) exchange(ctx context.Context, t *scope.DNSTarget, m *dns.Msg) error {
	cl := &dns.Client{Net: "udp", Timeout: c.Timeout}
	if t.KeyName != "" {
		alg, ok := tsigAlgorithms[t.Algorithm]
		if !ok {
			return fmt.Errorf("unsupported TSIG algorithm %q", t.Algorithm)
		}
		key := fqdn(t.KeyName)
		cl.TsigSecret = map[string]string{key: t.Secret}
		m.SetTsig(key, alg, 300, c.now().Unix())
	}
	r, _, err := cl.ExchangeContext(ctx, m, t.Server)
	if err == nil && r.Truncated {
		cl.Net = "tcp"
		r, _, err = cl.ExchangeContext(ctx, m, t.Server)
	}
	if err != nil {
		return fmt.Errorf("update to %s: %w", t.Server, err)
	}
	switch r.Rcode {
	case dns.RcodeSuccess:
		return nil
	case dns.RcodeYXDomain, dns.RcodeYXRrset, dns.RcodeNXRrset, dns.RcodeNameError:
		return errPrereq{r.Rcode}
	}
	return fmt.Errorf("%s answered %s for zone %s", t.Server, dns.RcodeToString[r.Rcode], t.Zone)
}

func (c *Client) update(t *scope.DNSTarget) *dns.Msg {
	m := new(dns.Msg)
	m.SetUpdate(fqdn(t.Zone))
	return m
}

// AddForward registers name -> ip. With a DHCID (name protection, RFC 4703
// section 5.3) the A record is only added when the name is unused or
// already belongs to the same client; otherwise ErrConflict is returned.
// Without DHCID existing A records of the name are replaced.
func (c *Client) AddForward(ctx context.Context, t *scope.DNSTarget, name string, ip netip.Addr, dhcid string, ttl uint32) error {
	if dhcid == "" {
		m := c.update(t)
		m.RemoveRRset([]dns.RR{rrset(name, dns.TypeA)})
		m.Insert([]dns.RR{aRR(name, ip, ttl)})
		return c.exchange(ctx, t, m)
	}
	m := c.update(t)
	m.NameNotUsed([]dns.RR{rrset(name, dns.TypeANY)})
	m.Insert([]dns.RR{aRR(name, ip, ttl), dhcidRR(name, dhcid, ttl)})
	err := c.exchange(ctx, t, m)
	var pe errPrereq
	if !errors.As(err, &pe) {
		return err
	}
	// The name exists: take it over only if it carries our DHCID.
	m = c.update(t)
	m.Used([]dns.RR{dhcidRR(name, dhcid, 0)})
	m.RemoveRRset([]dns.RR{rrset(name, dns.TypeA)})
	m.Insert([]dns.RR{aRR(name, ip, ttl)})
	err = c.exchange(ctx, t, m)
	if errors.As(err, &pe) {
		return ErrConflict
	}
	return err
}

// RemoveForward deletes the A record name -> ip. With a DHCID only if the
// name still belongs to the client; the DHCID record is removed with the
// last address record (RFC 4703 section 5.5).
func (c *Client) RemoveForward(ctx context.Context, t *scope.DNSTarget, name string, ip netip.Addr, dhcid string) error {
	m := c.update(t)
	if dhcid != "" {
		m.Used([]dns.RR{dhcidRR(name, dhcid, 0)})
	}
	m.Remove([]dns.RR{aRR(name, ip, 0)})
	err := c.exchange(ctx, t, m)
	var pe errPrereq
	if errors.As(err, &pe) || dhcid == "" {
		// Not ours (any more): nothing to clean up.
		return nil
	}
	if err != nil {
		return err
	}
	m = c.update(t)
	m.Used([]dns.RR{dhcidRR(name, dhcid, 0)})
	m.RRsetNotUsed([]dns.RR{rrset(name, dns.TypeA), rrset(name, dns.TypeAAAA)})
	m.RemoveRRset([]dns.RR{rrset(name, dns.TypeDHCID)})
	if err := c.exchange(ctx, t, m); err != nil && !errors.As(err, &pe) {
		return err
	}
	return nil
}

// AddReverse sets the PTR record of ip to name (replacing any other).
func (c *Client) AddReverse(ctx context.Context, t *scope.DNSTarget, ip netip.Addr, name string, ttl uint32) error {
	rr := ptrRR(ip, name, ttl)
	m := c.update(t)
	m.RemoveRRset([]dns.RR{rrset(rr.Header().Name, dns.TypePTR)})
	m.Insert([]dns.RR{rr})
	return c.exchange(ctx, t, m)
}

// RemoveReverse deletes the PTR record ip -> name; a PTR record that
// points to another name (the address was re-leased) stays.
func (c *Client) RemoveReverse(ctx context.Context, t *scope.DNSTarget, ip netip.Addr, name string) error {
	m := c.update(t)
	m.Remove([]dns.RR{ptrRR(ip, name, 0)})
	err := c.exchange(ctx, t, m)
	var pe errPrereq
	if errors.As(err, &pe) {
		return nil
	}
	return err
}

// ReverseName returns the in-addr.arpa name of ip (without trailing dot).
func ReverseName(ip netip.Addr) string {
	rev, _ := dns.ReverseAddr(ip.String())
	return strings.TrimSuffix(rev, ".")
}

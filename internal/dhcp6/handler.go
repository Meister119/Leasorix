// Package dhcp6 implements a stateful DHCPv6 server (RFC 8415): address
// assignment (IA_NA), prefix delegation (IA_PD), relayed messages and
// stateless configuration (INFORMATION-REQUEST).
package dhcp6

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope6"
)

// Ports used by DHCPv6.
const (
	ServerPort = 547
	ClientPort = 546
)

// Request is a received message with its context.
type Request struct {
	Msg dhcpv6.DHCPv6
	// IfName is the receiving interface, IfAddrs its global/ULA prefixes.
	IfName  string
	IfAddrs []netip.Prefix
	// Src is the sender (client link-local address or relay).
	Src netip.AddrPort
	// Dst is the destination address (multicast or one of ours).
	Dst netip.Addr
}

// Response is a message and where to send it.
type Response struct {
	Msg dhcpv6.DHCPv6
	Dst netip.AddrPort
}

// ScopeSource provides the DHCPv6 snapshot.
type ScopeSource interface {
	Snapshot() *scope6.Snapshot
}

// Partner decides whether this server answers a client in a high
// availability relationship (the DUID is the client key).
type Partner interface {
	Accept(clientKey string, directed bool) (bool, string)
}

// Filter applies the MAC allow/deny lists to clients whose hardware
// address is known.
type Filter func(mac string) (denied bool, reason string)

// Handler is the DHCPv6 message processing, independent of sockets.
type Handler struct {
	log      *slog.Logger
	scopes   ScopeSource
	leases   *lease6.Manager
	serverID dhcpv6.DUID
	metrics  *metrics.DHCP6
	partner  Partner
	filter   Filter
}

// NewHandler creates a handler answering with serverID.
func NewHandler(log *slog.Logger, scopes ScopeSource, leases *lease6.Manager, serverID dhcpv6.DUID) *Handler {
	return &Handler{log: log, scopes: scopes, leases: leases, serverID: serverID}
}

// SetMetrics enables metrics.
func (h *Handler) SetMetrics(m *metrics.DHCP6) { h.metrics = m }

// SetPartner enables the HA client split.
func (h *Handler) SetPartner(p Partner) { h.partner = p }

// SetFilter enables MAC filtering.
func (h *Handler) SetFilter(f Filter) { h.filter = f }

// ServerID returns the server DUID.
func (h *Handler) ServerID() dhcpv6.DUID { return h.serverID }

// txn is the context of one client message.
type txn struct {
	log     *slog.Logger
	req     *Request
	msg     *dhcpv6.Message
	relay   *dhcpv6.RelayMessage // outermost relay, nil if direct
	duid    string
	pools   []*scope6.Pool // enabled scopes of the client's link
	snap    *scope6.Snapshot
	host    string
	metrics *metrics.DHCP6
}

func (t *txn) drop(reason string) *Response {
	t.metrics.Dropped(reason)
	return nil
}

// Handle processes one message and returns the reply or nil.
func (h *Handler) Handle(ctx context.Context, req *Request) *Response {
	msg, err := req.Msg.GetInnerMessage()
	if err != nil {
		h.metrics.Dropped("malformed")
		return nil
	}
	kind := msg.Type().String()
	h.metrics.Received(kind)
	t := &txn{req: req, msg: msg, snap: h.scopes.Snapshot(), metrics: h.metrics}
	if req.Msg.IsRelay() {
		t.relay = req.Msg.(*dhcpv6.RelayMessage)
	}
	switch msg.Type() {
	case dhcpv6.MessageTypeSolicit, dhcpv6.MessageTypeRequest, dhcpv6.MessageTypeConfirm, dhcpv6.MessageTypeRenew,
		dhcpv6.MessageTypeRebind, dhcpv6.MessageTypeRelease, dhcpv6.MessageTypeDecline, dhcpv6.MessageTypeInformationRequest:
	default:
		return t.drop("not_request")
	}
	if cid := msg.Options.ClientID(); cid != nil {
		t.duid = hex.EncodeToString(cid.ToBytes())
	} else if msg.Type() != dhcpv6.MessageTypeInformationRequest {
		return t.drop("invalid")
	}
	t.log = h.log.With("type", kind, "duid", t.duid, "xid", msg.TransactionID.String(), "if", req.IfName)

	// Server identifier rules (RFC 8415 section 16).
	sid := msg.Options.ServerID()
	ours := sid != nil && sid.Equal(h.serverID)
	switch msg.Type() {
	case dhcpv6.MessageTypeSolicit, dhcpv6.MessageTypeConfirm, dhcpv6.MessageTypeRebind:
		if sid != nil {
			return t.drop("invalid")
		}
	case dhcpv6.MessageTypeRequest, dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRelease, dhcpv6.MessageTypeDecline:
		if !ours {
			return t.drop("other_server")
		}
	case dhcpv6.MessageTypeInformationRequest:
		if sid != nil && !ours {
			return t.drop("other_server")
		}
	}

	link, mac := h.linkOf(req)
	if mac == "" {
		mac = macFromDUID(msg.Options.ClientID())
	}
	if mac != "" && h.filter != nil {
		if denied, why := h.filter(mac); denied {
			t.log.Info("client filtered, ignoring", "mac", mac, "reason", why)
			return t.drop("filtered")
		}
	}
	if h.partner != nil && t.duid != "" {
		if ok, why := h.partner.Accept(t.duid, ours); !ok {
			t.log.Debug("not answering, client is served by the HA partner", "reason", why)
			return t.drop(why)
		}
	}
	t.pools = h.poolsFor(t.snap, req, link)
	if len(t.pools) == 0 && msg.Type() != dhcpv6.MessageTypeInformationRequest {
		t.log.Debug("no IPv6 scope for the client's link", "link", link)
		return t.drop("no_scope")
	}
	if f := msg.Options.FQDN(); f != nil && f.DomainName != nil && len(f.DomainName.Labels) > 0 {
		t.host = sanitizeHost(f.DomainName.Labels[0])
	}
	ctx = audit.WithActor(ctx, audit.ActorDHCP, req.IfName)

	// Unicast is only allowed when we sent the Server Unicast option,
	// which we never do (RFC 8415 section 18.4).
	if t.relay == nil && req.Dst.IsValid() && !req.Dst.IsMulticast() {
		switch msg.Type() {
		case dhcpv6.MessageTypeRequest, dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRelease, dhcpv6.MessageTypeDecline:
			return h.respond(t, h.statusReply(t, iana.StatusUseMulticast, "use multicast"))
		}
	}

	var reply *dhcpv6.Message
	switch msg.Type() {
	case dhcpv6.MessageTypeSolicit:
		reply = h.solicit(ctx, t)
	case dhcpv6.MessageTypeRequest:
		reply = h.bind(ctx, t)
	case dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRebind:
		reply = h.renew(ctx, t)
	case dhcpv6.MessageTypeRelease:
		reply = h.release(ctx, t)
	case dhcpv6.MessageTypeDecline:
		reply = h.decline(ctx, t)
	case dhcpv6.MessageTypeConfirm:
		reply = h.confirm(t)
	case dhcpv6.MessageTypeInformationRequest:
		reply = h.inform(t)
	}
	if reply == nil {
		return nil
	}
	return h.respond(t, reply)
}

// linkOf returns the link address of the relay closest to the client (the
// innermost relay with a link address) and the client link-layer address
// a relay reported (RFC 6939).
func (h *Handler) linkOf(req *Request) (netip.Addr, string) {
	var link netip.Addr
	mac := ""
	m := req.Msg
	for m.IsRelay() {
		r := m.(*dhcpv6.RelayMessage)
		if a, ok := netip.AddrFromSlice(r.LinkAddr.To16()); ok && !a.IsUnspecified() && !a.IsLinkLocalUnicast() {
			link = a
		}
		if ht, ll := r.Options.ClientLinkLayerAddress(); ht == iana.HWTypeEthernet && len(ll) == 6 {
			mac = strings.ToLower(ll.String())
		}
		inner, err := dhcpv6.DecapsulateRelay(r)
		if err != nil {
			break
		}
		m = inner
	}
	return link, mac
}

// poolsFor returns the enabled scopes of the client's link: the scope of
// the relay link address, or the scopes whose prefix contains an address
// of the receiving interface.
func (h *Handler) poolsFor(snap *scope6.Snapshot, req *Request, link netip.Addr) []*scope6.Pool {
	var out []*scope6.Pool
	add := func(p *scope6.Pool) {
		if p == nil || !p.Enabled {
			return
		}
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	if req.Msg.IsRelay() {
		if link.IsValid() {
			add(snap.ForAddr(link))
		}
		return out
	}
	for _, a := range req.IfAddrs {
		add(snap.ForAddr(a.Addr()))
	}
	return out
}

func macFromDUID(d dhcpv6.DUID) string {
	switch v := d.(type) {
	case *dhcpv6.DUIDLLT:
		if v.HWType == iana.HWTypeEthernet && len(v.LinkLayerAddr) == 6 {
			return strings.ToLower(v.LinkLayerAddr.String())
		}
	case *dhcpv6.DUIDLL:
		if v.HWType == iana.HWTypeEthernet && len(v.LinkLayerAddr) == 6 {
			return strings.ToLower(v.LinkLayerAddr.String())
		}
	}
	return ""
}

func sanitizeHost(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 63; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b = append(b, c)
		}
	}
	return string(b)
}

// ---- reply construction ---------------------------------------------------------------

func (h *Handler) newReply(t *txn, mt dhcpv6.MessageType) *dhcpv6.Message {
	rep := &dhcpv6.Message{MessageType: mt, TransactionID: t.msg.TransactionID}
	if cid := t.msg.GetOneOption(dhcpv6.OptionClientID); cid != nil {
		rep.AddOption(cid)
	}
	rep.AddOption(dhcpv6.OptServerID(h.serverID))
	return rep
}

func (h *Handler) statusReply(t *txn, code iana.StatusCode, text string) *dhcpv6.Message {
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	rep.AddOption(&dhcpv6.OptStatusCode{StatusCode: code, StatusMessage: text})
	return rep
}

// respond addresses the reply: back through the relays, or to the client.
func (h *Handler) respond(t *txn, rep *dhcpv6.Message) *Response {
	t.metrics.Sent(rep.Type().String())
	if t.relay != nil {
		out, err := dhcpv6.NewRelayReplFromRelayForw(t.relay, rep)
		if err != nil {
			t.log.Warn("building relay reply", "err", err)
			return nil
		}
		port := t.req.Src.Port()
		if port == 0 {
			port = ServerPort
		}
		return &Response{Msg: out, Dst: netip.AddrPortFrom(t.req.Src.Addr(), port)}
	}
	port := t.req.Src.Port()
	if port == 0 {
		port = ClientPort
	}
	return &Response{Msg: rep, Dst: netip.AddrPortFrom(t.req.Src.Addr(), port)}
}

// addOptions adds the configured options: those in the client's option
// request, or all when it sent none.
func addOptions(rep *dhcpv6.Message, req *dhcpv6.Message, opts map[uint16][]byte) {
	oro := req.Options.RequestedOptions()
	add := func(code uint16) {
		if v, ok := opts[code]; ok && rep.GetOneOption(dhcpv6.OptionCode(code)) == nil {
			rep.AddOption(&dhcpv6.OptionGeneric{OptionCode: dhcpv6.OptionCode(code), OptionData: v})
		}
	}
	if len(oro) > 0 {
		for _, c := range oro {
			add(uint16(c))
		}
		return
	}
	codes := make([]uint16, 0, len(opts))
	for c := range opts {
		codes = append(codes, c)
	}
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j] < codes[j-1]; j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
	for _, c := range codes {
		add(c)
	}
}

// t1t2 are the renewal times: 0.5 and 0.8 of the preferred lifetime
// (RFC 8415 section 21.4).
func t1t2(preferred time.Duration) (time.Duration, time.Duration) {
	return (preferred / 2).Truncate(time.Second), (preferred * 8 / 10).Truncate(time.Second)
}

func remaining(until, now time.Time) time.Duration {
	if !until.After(now) {
		return 0
	}
	return until.Sub(now).Truncate(time.Second)
}

func ipNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), 128)}
}

func addrOf(ip net.IP) netip.Addr {
	a, _ := netip.AddrFromSlice(ip.To16())
	return a
}

func iaid(id [4]byte) uint32 {
	return uint32(id[0])<<24 | uint32(id[1])<<16 | uint32(id[2])<<8 | uint32(id[3])
}

package dhcp4

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/macaddr"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
)

// Ports used by DHCPv4.
const (
	ServerPort = 67
	ClientPort = 68
)

// Request is an incoming packet together with its receive context.
type Request struct {
	Msg *dhcpv4.DHCPv4
	// IfName is the interface the packet was received on.
	IfName string
	// IfAddrs are the IPv4 addresses (with prefix) of that interface.
	IfAddrs []netip.Prefix
	// Src is the UDP source of the packet.
	Src netip.AddrPort
	// Dst is the IP destination of the packet (broadcast or one of ours).
	Dst netip.Addr
}

// Response is a packet to send and how to address it.
type Response struct {
	Msg *dhcpv4.DHCPv4
	// Dst is the UDP destination for normal (L3) sends.
	Dst netip.AddrPort
	// Src is the source address to use (the server identifier).
	Src netip.Addr
	// L2Unicast asks the transport to deliver the packet to the client's
	// hardware address with IP destination yiaddr (client has no address
	// yet and did not ask for broadcast). The transport falls back to a
	// broadcast when it cannot do that.
	L2Unicast bool
}

// ScopeSource provides the current scope snapshot.
type ScopeSource interface {
	Snapshot() *scope.Snapshot
}

// Partner decides whether this server answers a client when it runs in a
// high availability relationship.
type Partner interface {
	// Accept reports whether to answer the client; directed is true for
	// messages explicitly addressed to this server. Otherwise it returns a
	// drop reason.
	Accept(mac string, directed bool) (bool, string)
}

// Handler implements the DHCPv4 server state machine (RFC 2131). It is
// independent of sockets and can be driven directly in tests.
type Handler struct {
	log     *slog.Logger
	scopes  ScopeSource
	leases  *lease.Manager
	metrics *metrics.DHCP4
	partner Partner
}

// SetMetrics enables packet metrics.
func (h *Handler) SetMetrics(m *metrics.DHCP4) { h.metrics = m }

// SetPartner enables HA client distribution.
func (h *Handler) SetPartner(p Partner) { h.partner = p }

// directed reports whether the message is explicitly addressed to this
// server: it carries one of our server identifiers, or it is a renewal
// unicast directly (not relayed) to one of our addresses.
func (h *Handler) directed(req *Request) bool {
	m := req.Msg
	if sid := m.ServerIdentifier(); !isZero(sid) {
		return h.isOurID(req, sid)
	}
	if !isZero(m.GatewayIPAddr) || isZero(m.ClientIPAddr) || !req.Dst.IsValid() || req.Dst == bcastAddr {
		return false
	}
	for _, a := range req.IfAddrs {
		if a.Addr() == req.Dst {
			return true
		}
	}
	return false
}

// NewHandler creates a handler.
func NewHandler(log *slog.Logger, scopes ScopeSource, leases *lease.Manager) *Handler {
	return &Handler{log: log, scopes: scopes, leases: leases}
}

// Handle processes one request and returns the response or nil.
func (h *Handler) Handle(ctx context.Context, req *Request) *Response {
	m := req.Msg
	if m.OpCode != dhcpv4.OpcodeBootRequest {
		h.metrics.Dropped("not_request")
		return nil
	}
	mt := m.MessageType()
	if mt == dhcpv4.MessageTypeNone {
		// BOOTP is not supported (optional, phase 7).
		h.metrics.Received("BOOTP")
		h.metrics.Dropped("bootp")
		return nil
	}
	start := time.Now()
	h.metrics.Received(mt.String())
	defer func() { h.metrics.Observe(mt.String(), time.Since(start)) }()
	if len(m.ClientHWAddr) == 0 || len(m.ClientHWAddr) > 16 {
		h.metrics.Dropped("invalid")
		return nil
	}
	c := clientOf(m)
	source := req.IfName
	if !isZero(m.GatewayIPAddr) {
		source += " via " + m.GatewayIPAddr.String()
	}
	ctx = audit.WithActor(ctx, audit.ActorDHCP, source)
	log := h.log.With("type", mt.String(), "mac", c.MAC, "xid", m.TransactionID.String(), "if", req.IfName)
	if !isZero(m.GatewayIPAddr) {
		log = log.With("giaddr", m.GatewayIPAddr.String())
	}

	snap := h.scopes.Snapshot()
	if denied, why := snap.Filtered(c.MAC); denied {
		log.Info("client filtered, ignoring", "reason", why)
		h.metrics.Dropped("filtered")
		return nil
	}
	if h.partner != nil {
		if ok, why := h.partner.Accept(c.MAC, h.directed(req)); !ok {
			log.Debug("not answering, client is served by the HA partner", "reason", why)
			h.metrics.Dropped(why)
			return nil
		}
	}
	tx := &txn{log: log, req: req, snap: snap, client: c, info: clientInfoOf(m, c), metrics: h.metrics}

	unlock := h.leases.LockClient(c)
	defer unlock()

	switch mt {
	case dhcpv4.MessageTypeDiscover:
		return h.discover(ctx, tx)
	case dhcpv4.MessageTypeRequest:
		return h.request(ctx, tx)
	case dhcpv4.MessageTypeDecline:
		h.decline(ctx, tx)
	case dhcpv4.MessageTypeRelease:
		h.release(ctx, tx)
	case dhcpv4.MessageTypeInform:
		return h.inform(tx)
	default:
		log.Debug("ignoring message type")
	}
	return nil
}

// txn bundles the per-request context.
type txn struct {
	log     *slog.Logger
	req     *Request
	snap    *scope.Snapshot
	client  lease.Client
	info    *scope.ClientInfo
	metrics *metrics.DHCP4
}

// drop records why a packet is not answered and returns nil.
func (t *txn) drop(reason string) *Response {
	t.metrics.Dropped(reason)
	return nil
}

func clientOf(m *dhcpv4.DHCPv4) lease.Client {
	return lease.Client{
		MAC:      macaddr.FromHW(m.ClientHWAddr),
		ClientID: macaddr.ClientIDFromBytes(m.Options.Get(dhcpv4.OptionClientIdentifier)),
		Hostname: sanitizeHostname(m.HostName()),
	}
}

func sanitizeHostname(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 63; i++ {
		ch := s[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '_' {
			b = append(b, ch)
		}
	}
	return string(b)
}

func isZero(ip net.IP) bool { return ip == nil || ip.IsUnspecified() }

func u32(ip net.IP) uint32 {
	v, _ := ip4.FromIP(ip)
	return v
}

// selectionAddr returns the address that identifies the client's link for
// relayed / explicitly selected subnets: option 118 (RFC 3011), relay
// sub-option 5 link-selection (RFC 3527), then giaddr.
func selectionAddr(m *dhcpv4.DHCPv4) uint32 {
	if v := m.Options.Get(dhcpv4.OptionSubnetSelection); len(v) == 4 {
		return u32(net.IP(v))
	}
	if rai := m.RelayAgentInfo(); rai != nil {
		if v := rai.Get(dhcpv4.LinkSelectionSubOption); len(v) == 4 {
			return u32(net.IP(v))
		}
	}
	return u32(m.GatewayIPAddr)
}

// linkPools returns the enabled scopes serving the client's link.
func (h *Handler) linkPools(snap *scope.Snapshot, req *Request) []*scope.Pool {
	var out []*scope.Pool
	add := func(p *scope.Pool) {
		if p == nil {
			return
		}
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	if sel := selectionAddr(req.Msg); sel != 0 {
		add(snap.ForAddr(sel))
		return out
	}
	for _, a := range req.IfAddrs {
		add(snap.ForAddr(ip4.MustFromAddr(a.Addr())))
	}
	return out
}

// serverID picks the server identifier for a reply concerning pool p.
func (h *Handler) serverID(req *Request, p *scope.Pool) netip.Addr {
	if rai := req.Msg.RelayAgentInfo(); rai != nil {
		// RFC 5107 server identifier override.
		if v := rai.Get(dhcpv4.ServerIdentifierOverrideSubOption); len(v) == 4 {
			return netip.AddrFrom4([4]byte(v))
		}
	}
	if req.Dst.IsValid() && req.Dst.Is4() && !req.Dst.IsMulticast() && req.Dst != netip.IPv4Unspecified() && req.Dst != bcastAddr {
		for _, a := range req.IfAddrs {
			if a.Addr() == req.Dst {
				return req.Dst
			}
		}
	}
	if p != nil {
		for _, a := range req.IfAddrs {
			if p.InSubnet(ip4.MustFromAddr(a.Addr())) {
				return a.Addr()
			}
		}
	}
	if len(req.IfAddrs) > 0 {
		return req.IfAddrs[0].Addr()
	}
	return netip.Addr{}
}

var bcastAddr = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// isOurID reports whether id is one of our addresses on the receiving
// interface (or the relay override).
func (h *Handler) isOurID(req *Request, id net.IP) bool {
	a, ok := netip.AddrFromSlice(id.To4())
	if !ok {
		return false
	}
	for _, p := range req.IfAddrs {
		if p.Addr() == a {
			return true
		}
	}
	if rai := req.Msg.RelayAgentInfo(); rai != nil {
		if v := rai.Get(dhcpv4.ServerIdentifierOverrideSubOption); len(v) == 4 && netip.AddrFrom4([4]byte(v)) == a {
			return true
		}
	}
	return false
}

// ---- DISCOVER ---------------------------------------------------------------

func (h *Handler) discover(ctx context.Context, t *txn) *Response {
	log, req, snap := t.log, t.req, t.snap
	pools := h.linkPools(snap, req)
	if len(pools) == 0 {
		log.Debug("no scope for client link")
		return t.drop("no_scope")
	}
	cands, evals := candidates(snap, pools, t.info)
	o := h.leases.Offer(ctx, t.client, cands, u32(req.Msg.RequestedIPAddress()))
	if o == nil {
		log.Warn("no address available", "scopes", poolNames(pools))
		return t.drop("no_address")
	}
	sid := h.serverID(req, o.Pool)
	if !sid.IsValid() {
		log.Warn("no server identifier available on interface")
		return t.drop("no_server_id")
	}
	reply, err := dhcpv4.NewReplyFromRequest(req.Msg,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer),
		dhcpv4.WithYourIP(ip4.ToIP(o.IP)),
		dhcpv4.WithServerIP(net.IPv4zero),
	)
	if err != nil {
		log.Error("building offer", "err", err)
		return nil
	}
	ev := evals[o.Pool.ID]
	h.addLeaseOptions(reply, o.Pool, sid, o.LeaseTime)
	addConfiguredOptions(log, reply, req.Msg, snap.Options(ev, o.Reservation))
	log.Debug("offer", "ip", ip4.String(o.IP), "scope", o.Pool.Name, "policies", ev.PolicyNames())
	return h.addressed(req, reply, sid)
}

func poolNames(ps []*scope.Pool) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// addLeaseOptions adds the options every OFFER/ACK carries.
func (h *Handler) addLeaseOptions(reply *dhcpv4.DHCPv4, p *scope.Pool, sid netip.Addr, leaseTime time.Duration) {
	reply.UpdateOption(dhcpv4.OptServerIdentifier(sid.AsSlice()))
	reply.UpdateOption(dhcpv4.OptSubnetMask(net.CIDRMask(p.Bits, 32)))
	if leaseTime <= 0 || leaseTime >= dhcpv4.MaxLeaseTime {
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionIPAddressLeaseTime, []byte{0xff, 0xff, 0xff, 0xff}))
		return
	}
	leaseTime = leaseTime.Truncate(time.Second)
	reply.UpdateOption(dhcpv4.OptIPAddressLeaseTime(leaseTime))
	reply.UpdateOption(dhcpv4.OptRenewTimeValue(leaseTime / 2))
	reply.UpdateOption(dhcpv4.OptRebindingTimeValue(leaseTime * 7 / 8))
}

// ---- REQUEST ----------------------------------------------------------------

func (h *Handler) request(ctx context.Context, t *txn) *Response {
	log, req, snap, c := t.log, t.req, t.snap, t.client
	m := req.Msg
	sid := m.ServerIdentifier()
	requested := u32(m.RequestedIPAddress())
	ciaddr := u32(m.ClientIPAddr)

	var (
		pool  *scope.Pool
		ip    uint32
		state string
	)
	switch {
	case !isZero(sid):
		state = "selecting"
		pools := h.linkPools(snap, req)
		if !h.isOurID(req, sid) {
			// The client accepted another server's offer.
			h.leases.CancelOffer(c, pools)
			log.Debug("client selected another server", "server_id", sid.String())
			return t.drop("other_server")
		}
		ip = requested
		if ip == 0 {
			ip = ciaddr
		}
		pool = poolFor(pools, ip)
		if pool == nil {
			return h.nak(log, req, nil, "requested address is not on the client's network")
		}
	case requested != 0 && ciaddr == 0:
		state = "init-reboot"
		pools := h.linkPools(snap, req)
		if len(pools) == 0 {
			log.Debug("init-reboot on a link without scope; staying silent")
			return t.drop("no_scope")
		}
		ip = requested
		pool = poolFor(pools, ip)
		if pool == nil {
			return h.nak(log, req, pools[0], "requested address is not on the client's network")
		}
	case ciaddr != 0:
		state = "renewing"
		if req.Dst == bcastAddr || !isZero(m.GatewayIPAddr) {
			state = "rebinding"
		}
		ip = ciaddr
		pool = snap.ForAddr(ip)
		if pool == nil {
			log.Debug("renewal for an address outside our scopes; staying silent", "ciaddr", m.ClientIPAddr.String())
			return t.drop("not_ours")
		}
	default:
		log.Debug("malformed request (no server id, requested address or ciaddr)")
		return t.drop("invalid")
	}
	log = log.With("state", state, "ip", ip4.String(ip), "scope", pool.Name)

	ev := snap.Evaluate(pool, t.info)
	cand := lease.Candidate{Pool: pool, Ranges: ev.Ranges, LeaseTime: ev.LeaseTime}
	res, err := h.leases.Request(ctx, c, cand, ip)
	if err != nil {
		log.Error("persisting lease failed; not acknowledging", "err", err)
		t.metrics.Error("persist")
		return nil
	}
	if !res.Ack {
		return h.nak(log, req, pool, res.Reason)
	}
	sidAddr := h.serverID(req, pool)
	if !sidAddr.IsValid() {
		return nil
	}
	reply, err := dhcpv4.NewReplyFromRequest(m,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithYourIP(ip4.ToIP(ip)),
		dhcpv4.WithClientIP(m.ClientIPAddr),
		dhcpv4.WithServerIP(net.IPv4zero),
	)
	if err != nil {
		log.Error("building ack", "err", err)
		return nil
	}
	h.addLeaseOptions(reply, pool, sidAddr, res.LeaseTime)
	addConfiguredOptions(log, reply, m, snap.Options(&ev, pool.ReservationFor(c.MAC, c.ClientID)))
	log.Info("lease acknowledged", "expires", res.Lease.Expires, "hostname", res.Lease.Hostname, "policies", ev.PolicyNames())
	return h.addressed(req, reply, sidAddr)
}

func poolFor(pools []*scope.Pool, ip uint32) *scope.Pool {
	for _, p := range pools {
		if p.InSubnet(ip) {
			return p
		}
	}
	return nil
}

func (h *Handler) nak(log *slog.Logger, req *Request, p *scope.Pool, reason string) *Response {
	sid := h.serverID(req, p)
	if !sid.IsValid() {
		return nil
	}
	reply, err := dhcpv4.NewReplyFromRequest(req.Msg,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeNak),
		dhcpv4.WithServerIP(net.IPv4zero),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(sid.AsSlice())),
		dhcpv4.WithOption(dhcpv4.OptMessage(reason)),
	)
	if err != nil {
		return nil
	}
	log.Info("lease rejected (NAK)", "reason", reason)
	// RFC 2131 4.1: NAKs are broadcast, or sent to the relay with the
	// broadcast bit set.
	if !isZero(req.Msg.GatewayIPAddr) {
		reply.SetBroadcast()
		return &Response{Msg: reply, Src: sid, Dst: netip.AddrPortFrom(addrOf(req.Msg.GatewayIPAddr), ServerPort)}
	}
	return &Response{Msg: reply, Src: sid, Dst: netip.AddrPortFrom(bcastAddr, ClientPort)}
}

// ---- DECLINE / RELEASE --------------------------------------------------------

func (h *Handler) decline(ctx context.Context, t *txn) {
	log, req, c := t.log, t.req, t.client
	m := req.Msg
	if sid := m.ServerIdentifier(); !isZero(sid) && !h.isOurID(req, sid) {
		return
	}
	ip := u32(m.RequestedIPAddress())
	pool := t.snap.ForAddr(ip)
	if ip == 0 || pool == nil {
		return
	}
	ok, err := h.leases.Decline(ctx, c, pool, ip)
	switch {
	case err != nil:
		log.Error("persisting decline", "err", err)
	case ok:
		log.Warn("client declined address; address blocked", "ip", ip4.String(ip), "scope", pool.Name, "message", m.Message())
	default:
		log.Debug("decline for an address not bound to the client", "ip", ip4.String(ip))
	}
}

func (h *Handler) release(ctx context.Context, t *txn) {
	log, req, c := t.log, t.req, t.client
	m := req.Msg
	if sid := m.ServerIdentifier(); !isZero(sid) && !h.isOurID(req, sid) {
		return
	}
	ip := u32(m.ClientIPAddr)
	if ip == 0 {
		return
	}
	ok, err := h.leases.Release(ctx, c, ip)
	switch {
	case err != nil:
		log.Error("persisting release", "err", err)
	case ok:
		log.Info("lease released", "ip", ip4.String(ip))
	default:
		log.Debug("release for an address not bound to the client", "ip", ip4.String(ip))
	}
}

// ---- INFORM -------------------------------------------------------------------

func (h *Handler) inform(t *txn) *Response {
	log, req, snap := t.log, t.req, t.snap
	m := req.Msg
	ciaddr := u32(m.ClientIPAddr)
	if ciaddr == 0 {
		return nil
	}
	pool := snap.ForAddr(ciaddr)
	if pool == nil || !pool.Enabled {
		log.Debug("inform from address outside our scopes", "ciaddr", m.ClientIPAddr.String())
		return t.drop("no_scope")
	}
	sid := h.serverID(req, pool)
	if !sid.IsValid() {
		return nil
	}
	reply, err := dhcpv4.NewReplyFromRequest(m,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithClientIP(m.ClientIPAddr),
		dhcpv4.WithYourIP(net.IPv4zero),
		dhcpv4.WithServerIP(net.IPv4zero),
	)
	if err != nil {
		return nil
	}
	// RFC 2131 3.4: no lease time in the reply to DHCPINFORM.
	reply.UpdateOption(dhcpv4.OptServerIdentifier(sid.AsSlice()))
	reply.UpdateOption(dhcpv4.OptSubnetMask(net.CIDRMask(pool.Bits, 32)))
	ev := snap.Evaluate(pool, t.info)
	addConfiguredOptions(log, reply, m, snap.Options(&ev, pool.ReservationFor(t.client.MAC, t.client.ClientID)))
	log.Debug("inform answered", "ciaddr", m.ClientIPAddr.String(), "scope", pool.Name)
	if !isZero(m.GatewayIPAddr) {
		return &Response{Msg: reply, Src: sid, Dst: netip.AddrPortFrom(addrOf(m.GatewayIPAddr), ServerPort)}
	}
	return &Response{Msg: reply, Src: sid, Dst: netip.AddrPortFrom(addrOf(m.ClientIPAddr), ClientPort)}
}

// ---- addressing ---------------------------------------------------------------

// addressed applies RFC 2131 section 4.1 to decide where an OFFER/ACK goes.
func (h *Handler) addressed(req *Request, reply *dhcpv4.DHCPv4, sid netip.Addr) *Response {
	m := req.Msg
	r := &Response{Msg: reply, Src: sid}
	switch {
	case !isZero(m.GatewayIPAddr):
		r.Dst = netip.AddrPortFrom(addrOf(m.GatewayIPAddr), ServerPort)
	case !isZero(m.ClientIPAddr):
		r.Dst = netip.AddrPortFrom(addrOf(m.ClientIPAddr), ClientPort)
	case m.IsBroadcast():
		r.Dst = netip.AddrPortFrom(bcastAddr, ClientPort)
	default:
		r.Dst = netip.AddrPortFrom(addrOf(reply.YourIPAddr), ClientPort)
		r.L2Unicast = true
	}
	return r
}

func addrOf(ip net.IP) netip.Addr {
	a, _ := netip.AddrFromSlice(ip.To4())
	return a
}

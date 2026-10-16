package dhcp4

import (
	"context"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

// bootpVendorArea is the size of the BOOTP vendor extensions field
// (RFC 951/1497): magic cookie, options and end option must fit.
const bootpVendorArea = 64

// bootpOptionOrder lists the options sent to BOOTP clients by priority
// (RFC 1497 vendor extensions); the rest follows by code while space is left.
var bootpOptionOrder = []uint8{1, 3, 6, 12, 15, 2, 42, 4, 7, 9, 17, 28}

// bootp answers a BOOTREQUEST (RFC 951/1542). Clients with a reservation get
// their reserved address; others get a dynamic address from a scope with
// BOOTP enabled, for the BOOTP lease duration.
func (h *Handler) bootp(ctx context.Context, t *txn) *Response {
	log, req, snap, c := t.log, t.req, t.snap, t.client
	pools := h.linkPools(snap, req)
	if len(pools) == 0 {
		return t.drop("no_scope")
	}
	all, evals := candidates(snap, pools, t.info)
	var cands []lease.Candidate
	for _, cand := range all {
		p := cand.Pool
		if p.ReservationFor(c.MAC, c.ClientID) == nil && !p.BOOTP {
			continue
		}
		cand.LeaseTime = h.bootpLease
		cands = append(cands, cand)
	}
	if len(cands) == 0 {
		log.Debug("BOOTP request without reservation on a scope without BOOTP")
		return t.drop("bootp")
	}
	o := h.leases.Offer(ctx, c, cands, u32(req.Msg.ClientIPAddr))
	if o == nil {
		log.Warn("no address available for BOOTP client", "scopes", poolNames(pools))
		return t.drop("no_address")
	}
	cand := lease.Candidate{Pool: o.Pool, LeaseTime: h.bootpLease}
	for _, x := range cands {
		if x.Pool == o.Pool {
			cand = x
		}
	}
	res, err := h.leases.Request(ctx, c, cand, o.IP)
	if err != nil {
		log.Error("persisting BOOTP lease failed; not answering", "err", err)
		t.metrics.Error("persist")
		return nil
	}
	if !res.Ack {
		log.Info("BOOTP address rejected", "ip", ip4.String(o.IP), "reason", res.Reason)
		return t.drop("no_address")
	}
	sid := h.serverID(req, o.Pool)
	if !sid.IsValid() {
		return t.drop("no_server_id")
	}
	reply, err := dhcpv4.NewReplyFromRequest(req.Msg, dhcpv4.WithYourIP(ip4.ToIP(o.IP)), dhcpv4.WithServerIP(net.IPv4zero))
	if err != nil {
		log.Error("building BOOTP reply", "err", err)
		return nil
	}
	reply.Options = dhcpv4.Options{}
	addBOOTPOptions(reply, o.Pool, snap.Options(evals[o.Pool.ID], o.Reservation))
	log.Info("BOOTP address assigned", "ip", ip4.String(o.IP), "scope", o.Pool.Name, "expires", res.Lease.Expires)
	return h.addressed(req, reply, sid)
}

// addBOOTPOptions fills the vendor area (64 bytes) with the most important
// options and mirrors boot server / file into the header.
func addBOOTPOptions(reply *dhcpv4.DHCPv4, p *scope.Pool, opts map[uint8][]byte) {
	if opts == nil {
		opts = map[uint8][]byte{}
	}
	if _, ok := opts[1]; !ok {
		opts[1] = net.CIDRMask(p.Bits, 32)
	}
	used := 4 + 1 // magic cookie + end
	add := func(code uint8) {
		v, ok := opts[code]
		if !ok || code == 0 || code == 255 || code >= 50 && code <= 61 {
			return
		}
		if used+2+len(v) > bootpVendorArea || reply.Options.Has(dhcpv4.GenericOptionCode(code)) {
			return
		}
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.GenericOptionCode(code), v))
		used += 2 + len(v)
	}
	for _, code := range bootpOptionOrder {
		add(code)
	}
	for code := 1; code < 255; code++ {
		add(uint8(code))
	}
	if v := opts[dhcpv4.OptionBootfileName.Code()]; len(v) > 0 {
		reply.BootFileName = truncate(string(v), 127)
	}
	if v := opts[dhcpv4.OptionTFTPServerName.Code()]; len(v) > 0 {
		reply.ServerHostName = truncate(string(v), 63)
		if ip := net.ParseIP(string(v)).To4(); ip != nil {
			reply.ServerIPAddr = ip
		}
	}
	if v := opts[150]; len(v) >= 4 && isZero(reply.ServerIPAddr) {
		reply.ServerIPAddr = net.IP(v[:4])
	}
}

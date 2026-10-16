package dhcp6

import (
	"context"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/scope6"
)

// life is the lifetime sent for a binding: the rest until its end, never
// more than configured (now is taken before the binding is made).
func life(until, now time.Time, configured time.Duration) time.Duration {
	return min(remaining(until, now), configured)
}

// assignResult is what one IA got.
type assignResult struct {
	lease lease6.Lease
	pool  *scope6.Pool
	ok    bool
}

// assign tries the client's scopes in order.
func (h *Handler) assign(ctx context.Context, t *txn, c lease6.Client, hints []netip.Prefix, commit bool) (assignResult, error) {
	for _, p := range t.pools {
		if c.Kind == lease6.KindNA && !p.HasAddresses() && p.ReservationFor(c.DUID, c.IAID) == nil || c.Kind == lease6.KindPD && !p.HasPD() {
			continue
		}
		l, ok, err := h.leases.Assign(ctx, lease6.Request{Client: c, Pool: p, Hints: hints, Hostname: t.host}, commit)
		if err != nil {
			return assignResult{}, err
		}
		if ok {
			return assignResult{l, p, true}, nil
		}
	}
	return assignResult{}, nil
}

func naOption(id [4]byte, r assignResult, now time.Time, extra ...dhcpv6.Option) *dhcpv6.OptIANA {
	ia := &dhcpv6.OptIANA{IaId: id}
	ia.Options.Options = append(ia.Options.Options, extra...)
	if r.ok {
		ia.T1, ia.T2 = t1t2(r.pool.PreferredLifetime)
		ia.Options.Options = append(ia.Options.Options, &dhcpv6.OptIAAddress{
			IPv6Addr: r.lease.Addr().AsSlice(), PreferredLifetime: life(r.lease.Preferred, now, r.pool.PreferredLifetime), ValidLifetime: life(r.lease.Expires, now, r.pool.ValidLifetime),
		})
	}
	return ia
}

func pdOption(id [4]byte, r assignResult, now time.Time, extra ...dhcpv6.Option) *dhcpv6.OptIAPD {
	ia := &dhcpv6.OptIAPD{IaId: id}
	ia.Options.Options = append(ia.Options.Options, extra...)
	if r.ok {
		ia.T1, ia.T2 = t1t2(r.pool.PreferredLifetime)
		ia.Options.Options = append(ia.Options.Options, &dhcpv6.OptIAPrefix{
			Prefix: ipNet(r.lease.Prefix), PreferredLifetime: life(r.lease.Preferred, now, r.pool.PreferredLifetime), ValidLifetime: life(r.lease.Expires, now, r.pool.ValidLifetime),
		})
	}
	return ia
}

func status(code iana.StatusCode, text string) *dhcpv6.OptStatusCode {
	return &dhcpv6.OptStatusCode{StatusCode: code, StatusMessage: text}
}

func naHints(ia *dhcpv6.OptIANA) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range ia.Options.Addresses() {
		if ip := addrOf(a.IPv6Addr); ip.IsValid() && !ip.IsUnspecified() {
			out = append(out, netip.PrefixFrom(ip, 128))
		}
	}
	return out
}

func pdHints(ia *dhcpv6.OptIAPD) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ia.Options.Prefixes() {
		if p.Prefix == nil {
			continue
		}
		bits, _ := p.Prefix.Mask.Size()
		if ip := addrOf(p.Prefix.IP); ip.IsValid() && !ip.IsUnspecified() && bits > 0 {
			out = append(out, netip.PrefixFrom(ip, bits).Masked())
		}
	}
	return out
}

// assignAll handles the IAs of SOLICIT and REQUEST. It returns false if no
// IA got anything.
func (h *Handler) assignAll(ctx context.Context, t *txn, rep *dhcpv6.Message, commit bool) (bool, error) {
	now := time.Now().Truncate(time.Second)
	any := false
	for _, ia := range t.msg.Options.IANA() {
		r, err := h.assign(ctx, t, lease6.Client{DUID: t.duid, IAID: iaid(ia.IaId), Kind: lease6.KindNA}, naHints(ia), commit)
		if err != nil {
			return false, err
		}
		if r.ok {
			any = true
			rep.AddOption(naOption(ia.IaId, r, now))
		} else {
			rep.AddOption(naOption(ia.IaId, r, now, status(iana.StatusNoAddrsAvail, "no addresses available")))
		}
	}
	for _, ia := range t.msg.Options.IAPD() {
		r, err := h.assign(ctx, t, lease6.Client{DUID: t.duid, IAID: iaid(ia.IaId), Kind: lease6.KindPD}, pdHints(ia), commit)
		if err != nil {
			return false, err
		}
		if r.ok {
			any = true
			rep.AddOption(pdOption(ia.IaId, r, now))
		} else {
			rep.AddOption(pdOption(ia.IaId, r, now, status(iana.StatusNoPrefixAvail, "no prefixes available")))
		}
	}
	return any, nil
}

// clientOptions returns the configured options for the client: server and
// scope level, plus the reservation of the client in that scope.
func (h *Handler) clientOptions(t *txn) map[uint16][]byte {
	var pool *scope6.Pool
	var res *scope6.Reservation
	if len(t.pools) > 0 {
		pool = t.pools[0]
		for _, ia := range t.msg.Options.IANA() {
			if res = pool.ReservationFor(t.duid, iaid(ia.IaId)); res != nil {
				break
			}
		}
	}
	return t.snap.Options(pool, res)
}

func (h *Handler) solicit(ctx context.Context, t *txn) *dhcpv6.Message {
	rapid := t.msg.GetOneOption(dhcpv6.OptionRapidCommit) != nil && t.pools[0].RapidCommit
	mt := dhcpv6.MessageTypeAdvertise
	if rapid {
		mt = dhcpv6.MessageTypeReply
	}
	rep := h.newReply(t, mt)
	if rapid {
		rep.AddOption(&dhcpv6.OptionGeneric{OptionCode: dhcpv6.OptionRapidCommit})
	}
	any, err := h.assignAll(ctx, t, rep, rapid)
	if err != nil {
		t.log.Error("persisting DHCPv6 binding failed; not answering", "err", err)
		return t.dropMsg("persist")
	}
	if !any {
		rep.AddOption(status(iana.StatusNoAddrsAvail, "no addresses available"))
		t.log.Warn("no IPv6 address or prefix available", "scopes", poolNames(t.pools))
	}
	if !rapid && t.pools[0].Preference > 0 {
		rep.AddOption(dhcpv6.OptPreference(t.pools[0].Preference))
	}
	addOptions(rep, t.msg, h.clientOptions(t))
	if rapid && any {
		t.log.Info("DHCPv6 binding committed (rapid commit)", "bindings", bindings(rep))
	} else {
		t.log.Debug("advertise", "bindings", bindings(rep))
	}
	return rep
}

func (t *txn) dropMsg(reason string) *dhcpv6.Message {
	t.metrics.Dropped(reason)
	return nil
}

// bind handles REQUEST.
func (h *Handler) bind(ctx context.Context, t *txn) *dhcpv6.Message {
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	any, err := h.assignAll(ctx, t, rep, true)
	if err != nil {
		t.log.Error("persisting DHCPv6 binding failed; not answering", "err", err)
		return t.dropMsg("persist")
	}
	addOptions(rep, t.msg, h.clientOptions(t))
	if any {
		t.log.Info("DHCPv6 binding acknowledged", "bindings", bindings(rep), "hostname", t.host)
	} else {
		t.log.Warn("no IPv6 address or prefix available", "scopes", poolNames(t.pools))
	}
	return rep
}

func poolFor(pools []*scope6.Pool, p netip.Prefix, kind lease6.Kind) *scope6.Pool {
	for _, pool := range pools {
		if kind == lease6.KindNA && pool.Prefix.Contains(p.Addr()) || kind == lease6.KindPD && pool.HasPD() && pool.PDPool.Contains(p.Addr()) {
			return pool
		}
	}
	return nil
}

// refresh extends the bindings of one IA (RENEW, REBIND). Addresses that
// are not appropriate for the link or cannot be kept are returned with
// lifetime 0 (the client stops using them); the client gets a new binding
// where possible.
func (h *Handler) refresh(ctx context.Context, t *txn, c lease6.Client, held []netip.Prefix, now time.Time) (kept []assignResult, dropped []netip.Prefix, err error) {
	for _, p := range held {
		pool := poolFor(t.pools, p, c.Kind)
		if pool == nil {
			dropped = append(dropped, p)
			continue
		}
		l, ok, err := h.leases.Extend(ctx, c, pool, p, t.host)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			kept = append(kept, assignResult{l, pool, true})
			continue
		}
		dropped = append(dropped, p)
	}
	if len(kept) == 0 {
		// Unknown binding (e.g. served by the HA partner, or expired):
		// create one, the old address if it is free.
		r, err := h.assign(ctx, t, c, held, true)
		if err != nil {
			return nil, nil, err
		}
		if r.ok {
			kept = append(kept, r)
			for i, p := range dropped {
				if p == r.lease.Prefix {
					dropped = append(dropped[:i], dropped[i+1:]...)
					break
				}
			}
		}
	}
	return kept, dropped, nil
}

func (h *Handler) renew(ctx context.Context, t *txn) *dhcpv6.Message {
	now := time.Now().Truncate(time.Second)
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	rebind := t.msg.Type() == dhcpv6.MessageTypeRebind
	for _, ia := range t.msg.Options.IANA() {
		c := lease6.Client{DUID: t.duid, IAID: iaid(ia.IaId), Kind: lease6.KindNA}
		kept, dropped, err := h.refresh(ctx, t, c, naHints(ia), now)
		if err != nil {
			t.log.Error("persisting DHCPv6 binding failed; not answering", "err", err)
			return t.dropMsg("persist")
		}
		out := &dhcpv6.OptIANA{IaId: ia.IaId}
		for _, r := range kept {
			out.T1, out.T2 = t1t2(r.pool.PreferredLifetime)
			out.Options.Options = append(out.Options.Options, &dhcpv6.OptIAAddress{
				IPv6Addr: r.lease.Addr().AsSlice(), PreferredLifetime: life(r.lease.Preferred, now, r.pool.PreferredLifetime), ValidLifetime: life(r.lease.Expires, now, r.pool.ValidLifetime),
			})
		}
		for _, p := range dropped {
			out.Options.Options = append(out.Options.Options, &dhcpv6.OptIAAddress{IPv6Addr: p.Addr().AsSlice()})
		}
		if len(kept) == 0 && !rebind {
			out.Options.Options = append(out.Options.Options, status(iana.StatusNoBinding, "no binding for this IA"))
		}
		rep.AddOption(out)
	}
	for _, ia := range t.msg.Options.IAPD() {
		c := lease6.Client{DUID: t.duid, IAID: iaid(ia.IaId), Kind: lease6.KindPD}
		kept, dropped, err := h.refresh(ctx, t, c, pdHints(ia), now)
		if err != nil {
			t.log.Error("persisting DHCPv6 binding failed; not answering", "err", err)
			return t.dropMsg("persist")
		}
		out := &dhcpv6.OptIAPD{IaId: ia.IaId}
		for _, r := range kept {
			out.T1, out.T2 = t1t2(r.pool.PreferredLifetime)
			out.Options.Options = append(out.Options.Options, &dhcpv6.OptIAPrefix{
				Prefix: ipNet(r.lease.Prefix), PreferredLifetime: life(r.lease.Preferred, now, r.pool.PreferredLifetime), ValidLifetime: life(r.lease.Expires, now, r.pool.ValidLifetime),
			})
		}
		for _, p := range dropped {
			out.Options.Options = append(out.Options.Options, &dhcpv6.OptIAPrefix{Prefix: ipNet(p)})
		}
		if len(kept) == 0 && !rebind {
			out.Options.Options = append(out.Options.Options, status(iana.StatusNoBinding, "no binding for this IA"))
		}
		rep.AddOption(out)
	}
	addOptions(rep, t.msg, h.clientOptions(t))
	t.log.Info("DHCPv6 binding renewed", "bindings", bindings(rep))
	return rep
}

func (h *Handler) release(ctx context.Context, t *txn) *dhcpv6.Message {
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	type held struct {
		id    [4]byte
		kind  lease6.Kind
		items []netip.Prefix
	}
	var all []held
	for _, ia := range t.msg.Options.IANA() {
		all = append(all, held{ia.IaId, lease6.KindNA, naHints(ia)})
	}
	for _, ia := range t.msg.Options.IAPD() {
		all = append(all, held{ia.IaId, lease6.KindPD, pdHints(ia)})
	}
	for _, ia := range all {
		c := lease6.Client{DUID: t.duid, IAID: iaid(ia.id), Kind: ia.kind}
		found := false
		for _, p := range ia.items {
			ok, err := h.leases.Release(ctx, c, p)
			if err != nil {
				t.log.Error("persisting DHCPv6 release", "err", err)
			}
			found = found || ok
		}
		if !found {
			st := status(iana.StatusNoBinding, "no binding for this IA")
			if ia.kind == lease6.KindNA {
				rep.AddOption(&dhcpv6.OptIANA{IaId: ia.id, Options: dhcpv6.IdentityOptions{Options: []dhcpv6.Option{st}}})
			} else {
				rep.AddOption(&dhcpv6.OptIAPD{IaId: ia.id, Options: dhcpv6.PDOptions{Options: []dhcpv6.Option{st}}})
			}
		}
	}
	rep.AddOption(status(iana.StatusSuccess, "released"))
	t.log.Info("DHCPv6 binding released")
	return rep
}

func (h *Handler) decline(ctx context.Context, t *txn) *dhcpv6.Message {
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	for _, ia := range t.msg.Options.IANA() {
		c := lease6.Client{DUID: t.duid, IAID: iaid(ia.IaId), Kind: lease6.KindNA}
		found := false
		for _, p := range naHints(ia) {
			ok, err := h.leases.Decline(ctx, c, p)
			if err != nil {
				t.log.Error("persisting DHCPv6 decline", "err", err)
			}
			if ok {
				found = true
				t.log.Warn("client declined address (duplicate address detection); address blocked", "ip", p.Addr().String())
			}
		}
		if !found {
			rep.AddOption(&dhcpv6.OptIANA{IaId: ia.IaId, Options: dhcpv6.IdentityOptions{Options: []dhcpv6.Option{status(iana.StatusNoBinding, "no binding for this IA")}}})
		}
	}
	rep.AddOption(status(iana.StatusSuccess, "declined"))
	return rep
}

// confirm answers whether the client's addresses are still on its link.
func (h *Handler) confirm(t *txn) *dhcpv6.Message {
	var addrs []netip.Prefix
	for _, ia := range t.msg.Options.IANA() {
		addrs = append(addrs, naHints(ia)...)
	}
	if len(addrs) == 0 {
		return t.dropMsg("invalid")
	}
	for _, a := range addrs {
		if poolFor(t.pools, a, lease6.KindNA) == nil {
			return h.statusReply(t, iana.StatusNotOnLink, "address not on link")
		}
	}
	return h.statusReply(t, iana.StatusSuccess, "all addresses on link")
}

// inform answers INFORMATION-REQUEST with the configured options.
func (h *Handler) inform(t *txn) *dhcpv6.Message {
	rep := h.newReply(t, dhcpv6.MessageTypeReply)
	addOptions(rep, t.msg, h.clientOptions(t))
	return rep
}

func poolNames(ps []*scope6.Pool) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// bindings lists the addresses and prefixes of a reply for the log.
func bindings(rep *dhcpv6.Message) []string {
	var out []string
	for _, ia := range rep.Options.IANA() {
		for _, a := range ia.Options.Addresses() {
			if a.ValidLifetime > 0 {
				out = append(out, a.IPv6Addr.String())
			}
		}
	}
	for _, ia := range rep.Options.IAPD() {
		for _, p := range ia.Options.Prefixes() {
			if p.ValidLifetime > 0 && p.Prefix != nil {
				out = append(out, p.Prefix.String())
			}
		}
	}
	return out
}

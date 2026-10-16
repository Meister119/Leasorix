package dhcp6

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/scope6"
	"github.com/meister119/leasorix/internal/store"
)

type henv struct {
	t      testing.TB
	ctx    context.Context
	scopes *scope6.Store
	leases *lease6.Manager
	h      *Handler
	sc     scope6.Scope
}

var serverDUID = &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{0x02, 0, 0, 0, 0, 0xfe}}

func newHEnv(t testing.TB, mod func(*scope6.Scope)) *henv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.DiscardHandler)
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	s := scope6.NewStore(db)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	sc := scope6.Scope{
		Name: "lan6", Prefix: netip.MustParsePrefix("2001:db8:1::/64"),
		Start: netip.MustParseAddr("2001:db8:1::100"), End: netip.MustParseAddr("2001:db8:1::1ff"),
		PreferredLifetime: time.Hour, ValidLifetime: 2 * time.Hour, Enabled: true, Preference: 10,
		PDPool: netip.MustParsePrefix("2001:db8:100::/48"), PDLength: 56,
	}
	if mod != nil {
		mod(&sc)
	}
	if sc, err = s.CreateScope(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetOption(ctx, scope6.OptionValue{Owner: scope6.ServerOwner, Code: 23, Value: "2001:db8::53"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetOption(ctx, scope6.OptionValue{Owner: scope6.Owner{Kind: scope6.OwnerScope, ID: sc.ID}, Code: 24, Value: "lan.example"}); err != nil {
		t.Fatal(err)
	}
	m := lease6.NewManager(lease6.DefaultConfig(), log, db)
	s.OnChange(m.ScopesChanged)
	return &henv{t: t, ctx: ctx, scopes: s, leases: m, h: NewHandler(log, s, m, serverDUID), sc: sc}
}

func duid(last byte) dhcpv6.DUID {
	return &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: net.HardwareAddr{0x02, 0, 0, 0, 0x11, last}}
}

func ianaOpt(id byte, addrs ...string) *dhcpv6.OptIANA {
	ia := &dhcpv6.OptIANA{IaId: [4]byte{0, 0, 0, id}}
	for _, a := range addrs {
		ia.Options.Options = append(ia.Options.Options, &dhcpv6.OptIAAddress{IPv6Addr: net.ParseIP(a), PreferredLifetime: time.Hour, ValidLifetime: time.Hour})
	}
	return ia
}

func iapdOpt(id byte, prefixes ...string) *dhcpv6.OptIAPD {
	ia := &dhcpv6.OptIAPD{IaId: [4]byte{0, 0, 0, id}}
	for _, p := range prefixes {
		_, n, _ := net.ParseCIDR(p)
		ia.Options.Options = append(ia.Options.Options, &dhcpv6.OptIAPrefix{Prefix: n})
	}
	return ia
}

// msg builds a client message and parses it back from the wire format.
func msg(t *testing.T, mt dhcpv6.MessageType, client dhcpv6.DUID, opts ...dhcpv6.Option) dhcpv6.DHCPv6 {
	t.Helper()
	m := &dhcpv6.Message{MessageType: mt, TransactionID: dhcpv6.TransactionID{1, 2, 3}}
	if client != nil {
		m.AddOption(dhcpv6.OptClientID(client))
	}
	for _, o := range opts {
		m.AddOption(o)
	}
	parsed, err := dhcpv6.FromBytes(m.ToBytes())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

var (
	clientLL  = netip.MustParseAddrPort("[fe80::1]:546")
	multicast = netip.MustParseAddr("ff02::1:2")
)

func (e *henv) handle(m dhcpv6.DHCPv6, dst netip.Addr) *Response {
	e.t.Helper()
	return e.h.Handle(e.ctx, &Request{Msg: m, IfName: "eth0", IfAddrs: []netip.Prefix{netip.MustParsePrefix("2001:db8:1::1/64")}, Src: clientLL, Dst: dst})
}

func reply(t testing.TB, r *Response) *dhcpv6.Message {
	t.Helper()
	if r == nil {
		t.Fatal("no response")
	}
	// Check what a client receives: the wire format, parsed again.
	parsed, err := dhcpv6.FromBytes(r.Msg.ToBytes())
	if err != nil {
		t.Fatal(err)
	}
	m, err := parsed.GetInnerMessage()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func firstAddr(m *dhcpv6.Message) (netip.Addr, *dhcpv6.OptIAAddress) {
	for _, ia := range m.Options.IANA() {
		for _, a := range ia.Options.Addresses() {
			ip, _ := netip.AddrFromSlice(a.IPv6Addr.To16())
			return ip, a
		}
	}
	return netip.Addr{}, nil
}

func TestSolicitRequestRenewRelease(t *testing.T) {
	e := newHEnv(t, nil)
	c := duid(1)
	oro := dhcpv6.OptRequestedOption(dhcpv6.OptionDNSRecursiveNameServer, dhcpv6.OptionDomainSearchList)
	adv := reply(t, e.handle(msg(t, dhcpv6.MessageTypeSolicit, c, ianaOpt(1), iapdOpt(2), oro), multicast))
	if adv.Type() != dhcpv6.MessageTypeAdvertise || !adv.Options.ServerID().Equal(serverDUID) || adv.Options.Preference() != 10 {
		t.Fatalf("advertise %s", adv.Summary())
	}
	ip, a := firstAddr(adv)
	if !e.scopes.Snapshot().ByID(e.sc.ID).InRange(ip) || a.ValidLifetime != 2*time.Hour || a.PreferredLifetime != time.Hour {
		t.Fatalf("advertised address %s %+v", ip, a)
	}
	if ia := adv.Options.OneIANA(); ia.T1 != 30*time.Minute || ia.T2 != 48*time.Minute {
		t.Fatalf("T1/T2 %v %v", ia.T1, ia.T2)
	}
	pd := adv.Options.OneIAPD().Options.Prefixes()
	if len(pd) != 1 || !e.sc.PDPool.Contains(addrOf(pd[0].Prefix.IP)) {
		t.Fatalf("prefixes %v", pd)
	}
	if bits, _ := pd[0].Prefix.Mask.Size(); bits != 56 {
		t.Fatalf("delegated length %d", bits)
	}
	if dns := adv.Options.DNS(); len(dns) != 1 || dns[0].String() != "2001:db8::53" || adv.Options.DomainSearchList() == nil {
		t.Fatalf("options %s", adv.Summary())
	}
	if _, ok := e.leases.Get(netip.PrefixFrom(ip, 128)); ok {
		t.Fatal("advertise must not bind")
	}

	// REQUEST with the server id binds the advertised address.
	sid := dhcpv6.OptServerID(serverDUID)
	rep := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRequest, c, sid, ianaOpt(1, ip.String()), iapdOpt(2, pd[0].Prefix.String())), multicast))
	if got, _ := firstAddr(rep); rep.Type() != dhcpv6.MessageTypeReply || got != ip {
		t.Fatalf("reply %s", rep.Summary())
	}
	if l, ok := e.leases.Get(netip.PrefixFrom(ip, 128)); !ok || l.State != lease6.StateActive || l.DUID == "" {
		t.Fatalf("binding %+v", l)
	}
	// REQUEST for another server is ignored, as is a SOLICIT with server id.
	other := dhcpv6.OptServerID(duid(99))
	if r := e.handle(msg(t, dhcpv6.MessageTypeRequest, c, other, ianaOpt(1)), multicast); r != nil {
		t.Fatal("answered a request for another server")
	}
	if r := e.handle(msg(t, dhcpv6.MessageTypeSolicit, c, sid, ianaOpt(1)), multicast); r != nil {
		t.Fatal("answered a solicit with server id")
	}
	// Unicast REQUEST: UseMulticast.
	uc := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRequest, c, sid, ianaOpt(1, ip.String())), netip.MustParseAddr("2001:db8:1::1")))
	if st := uc.Options.Status(); st == nil || st.StatusCode != iana.StatusUseMulticast {
		t.Fatalf("unicast: %s", uc.Summary())
	}

	// RENEW extends; an address of another client is returned with
	// lifetime 0 together with a new one.
	ren := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRenew, c, sid, ianaOpt(1, ip.String())), multicast))
	if got, a := firstAddr(ren); got != ip || a.ValidLifetime != 2*time.Hour {
		t.Fatalf("renew %s", ren.Summary())
	}
	c2 := duid(2)
	ren2 := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRenew, c2, sid, ianaOpt(1, ip.String())), multicast))
	addrs := ren2.Options.OneIANA().Options.Addresses()
	if len(addrs) != 2 {
		t.Fatalf("renew of a foreign address %s", ren2.Summary())
	}
	var stolen, fresh bool
	for _, a := range addrs {
		if a.IPv6Addr.Equal(ip.AsSlice()) {
			stolen = a.ValidLifetime != 0
		} else if a.ValidLifetime > 0 {
			fresh = true
		}
	}
	if stolen || !fresh {
		t.Fatalf("foreign address kept or no new one: %s", ren2.Summary())
	}
	// REBIND of an address that is not on the link: lifetime 0.
	reb := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRebind, duid(3), ianaOpt(1, "2001:db8:99::5")), multicast))
	for _, a := range reb.Options.OneIANA().Options.Addresses() {
		if a.IPv6Addr.Equal(net.ParseIP("2001:db8:99::5")) && a.ValidLifetime != 0 {
			t.Fatalf("off-link address kept: %s", reb.Summary())
		}
	}

	// CONFIRM: on link and not on link.
	if st := reply(t, e.handle(msg(t, dhcpv6.MessageTypeConfirm, c, ianaOpt(1, ip.String())), multicast)).Options.Status(); st.StatusCode != iana.StatusSuccess {
		t.Fatalf("confirm on link: %v", st)
	}
	if st := reply(t, e.handle(msg(t, dhcpv6.MessageTypeConfirm, c, ianaOpt(1, "2001:db8:2::1")), multicast)).Options.Status(); st.StatusCode != iana.StatusNotOnLink {
		t.Fatalf("confirm off link: %v", st)
	}

	// RELEASE ends the binding; releasing again reports NoBinding.
	rel := reply(t, e.handle(msg(t, dhcpv6.MessageTypeRelease, c, sid, ianaOpt(1, ip.String())), multicast))
	if st := rel.Options.Status(); st == nil || st.StatusCode != iana.StatusSuccess {
		t.Fatalf("release %s", rel.Summary())
	}
	if l, _ := e.leases.Get(netip.PrefixFrom(ip, 128)); l.State != lease6.StateReleased {
		t.Fatalf("after release %+v", l)
	}
	rel = reply(t, e.handle(msg(t, dhcpv6.MessageTypeRelease, c, sid, ianaOpt(1, ip.String())), multicast))
	if st := rel.Options.OneIANA().Options.Status(); st == nil || st.StatusCode != iana.StatusNoBinding {
		t.Fatalf("second release %s", rel.Summary())
	}
}

func TestRapidCommitDeclineInformation(t *testing.T) {
	e := newHEnv(t, func(sc *scope6.Scope) { sc.RapidCommit = true })
	c := duid(5)
	rapid := &dhcpv6.OptionGeneric{OptionCode: dhcpv6.OptionRapidCommit}
	rep := reply(t, e.handle(msg(t, dhcpv6.MessageTypeSolicit, c, ianaOpt(1), rapid), multicast))
	ip, _ := firstAddr(rep)
	if rep.Type() != dhcpv6.MessageTypeReply || rep.GetOneOption(dhcpv6.OptionRapidCommit) == nil {
		t.Fatalf("rapid commit %s", rep.Summary())
	}
	if l, ok := e.leases.Get(netip.PrefixFrom(ip, 128)); !ok || l.State != lease6.StateActive {
		t.Fatal("rapid commit did not bind")
	}
	dec := reply(t, e.handle(msg(t, dhcpv6.MessageTypeDecline, c, dhcpv6.OptServerID(serverDUID), ianaOpt(1, ip.String())), multicast))
	if st := dec.Options.Status(); st == nil || st.StatusCode != iana.StatusSuccess {
		t.Fatalf("decline %s", dec.Summary())
	}
	if l, _ := e.leases.Get(netip.PrefixFrom(ip, 128)); l.State != lease6.StateDeclined {
		t.Fatalf("declined %+v", l)
	}
	// INFORMATION-REQUEST: options only, also without client id.
	inf := reply(t, e.handle(msg(t, dhcpv6.MessageTypeInformationRequest, nil), multicast))
	if len(inf.Options.IANA()) != 0 || len(inf.Options.DNS()) != 1 {
		t.Fatalf("information %s", inf.Summary())
	}
}

func TestRelayedSolicitAndFilter(t *testing.T) {
	e := newHEnv(t, nil)
	inner := msg(t, dhcpv6.MessageTypeSolicit, duid(7), ianaOpt(1))
	relay, err := dhcpv6.EncapsulateRelay(inner, dhcpv6.MessageTypeRelayForward, net.ParseIP("2001:db8:1::fe"), net.ParseIP("fe80::7"))
	if err != nil {
		t.Fatal(err)
	}
	relay.AddOption(dhcpv6.OptInterfaceID([]byte("port-7")))
	parsed, err := dhcpv6.FromBytes(relay.ToBytes())
	if err != nil {
		t.Fatal(err)
	}
	resp := e.h.Handle(e.ctx, &Request{Msg: parsed, IfName: "eth1", IfAddrs: []netip.Prefix{netip.MustParsePrefix("2001:db8:ff::1/64")},
		Src: netip.MustParseAddrPort("[2001:db8:ff::2]:547"), Dst: netip.MustParseAddr("2001:db8:ff::1")})
	if resp == nil || resp.Dst.String() != "[2001:db8:ff::2]:547" || !resp.Msg.IsRelay() {
		t.Fatalf("relay response %+v", resp)
	}
	rr := resp.Msg.(*dhcpv6.RelayMessage)
	if rr.Type() != dhcpv6.MessageTypeRelayReply || string(rr.Options.InterfaceID()) != "port-7" {
		t.Fatalf("relay reply %s", rr.Summary())
	}
	if ip, _ := firstAddr(reply(t, resp)); !e.scopes.Snapshot().ByID(e.sc.ID).InRange(ip) {
		t.Fatalf("relayed advertise %s", ip)
	}
	// A relay link outside all scopes: no answer.
	relay2, _ := dhcpv6.EncapsulateRelay(inner, dhcpv6.MessageTypeRelayForward, net.ParseIP("2001:db8:77::1"), net.ParseIP("fe80::7"))
	if r := e.h.Handle(e.ctx, &Request{Msg: relay2, IfName: "eth1", Src: netip.MustParseAddrPort("[2001:db8:ff::2]:547")}); r != nil {
		t.Fatal("answered a relay for an unknown link")
	}
	// MAC filter on the DUID-LL hardware address.
	e.h.SetFilter(func(mac string) (bool, string) { return mac == "02:00:00:00:11:08", "deny list" })
	if r := e.handle(msg(t, dhcpv6.MessageTypeSolicit, duid(8), ianaOpt(1)), multicast); r != nil {
		t.Fatal("filtered client answered")
	}
	if r := e.handle(msg(t, dhcpv6.MessageTypeSolicit, duid(9), ianaOpt(1)), multicast); r == nil {
		t.Fatal("unfiltered client not answered")
	}
}

func TestExhaustedScopeAdvertisesNoAddrs(t *testing.T) {
	e := newHEnv(t, func(sc *scope6.Scope) {
		sc.Start, sc.End = netip.MustParseAddr("2001:db8:1::100"), netip.MustParseAddr("2001:db8:1::100")
		sc.PDPool, sc.PDLength = netip.Prefix{}, 0
	})
	sid := dhcpv6.OptServerID(serverDUID)
	reply(t, e.handle(msg(t, dhcpv6.MessageTypeRequest, duid(1), sid, ianaOpt(1)), multicast))
	adv := reply(t, e.handle(msg(t, dhcpv6.MessageTypeSolicit, duid(2), ianaOpt(1), iapdOpt(2)), multicast))
	if st := adv.Options.Status(); st == nil || st.StatusCode != iana.StatusNoAddrsAvail {
		t.Fatalf("top level status: %s", adv.Summary())
	}
	if st := adv.Options.OneIANA().Options.Status(); st == nil || st.StatusCode != iana.StatusNoAddrsAvail {
		t.Fatalf("IA_NA status: %s", adv.Summary())
	}
	if st := adv.Options.OneIAPD().Options.Status(); st == nil || st.StatusCode != iana.StatusNoPrefixAvail {
		t.Fatalf("IA_PD status: %s", adv.Summary())
	}
}

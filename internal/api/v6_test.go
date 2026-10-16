package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/lease6"
)

func scope6Input(name, prefix string) gen.Scope6Input {
	start, end := ptr("2001:db8:1::100"), ptr("2001:db8:1::1ff")
	return gen.Scope6Input{Name: name, Prefix: prefix, Start: start, End: end, PreferredLifetime: 3600, ValidLifetime: 7200, Enabled: true,
		PdPool: ptr("2001:db8:100::/40"), PdLength: ptr(56)}
}

func TestIPv6API(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(201, "POST", "/users", gen.UserCreate{Username: "ro", Password: "readonly-pass", Role: gen.Readonly})
	ro := st.login("ro", "readonly-pass")

	var sc gen.Scope6
	a.must(201, "POST", "/v6/scopes", scope6Input("lan6", "2001:db8:1::/64")).json(t, &sc)
	if sc.Prefix != "2001:db8:1::/64" || sc.PdPool != "2001:db8:100::/40" || sc.PdLength != 56 || sc.ValidLifetime != 7200 {
		t.Fatalf("scope %+v", sc)
	}
	a.must(400, "POST", "/v6/scopes", scope6Input("bad", "10.0.0.0/24"))
	a.must(409, "POST", "/v6/scopes", scope6Input("other", "2001:db8:1::/64"))
	ro.must(403, "POST", "/v6/scopes", scope6Input("x", "2001:db8:9::/64"))
	in := scope6Input("lan6-new", "2001:db8:1::/64")
	in.RapidCommit, in.Preference = ptr(true), ptr(255)
	a.must(200, "PUT", fmt.Sprintf("/v6/scopes/%d", sc.Id), in).json(t, &sc)
	if sc.Name != "lan6-new" || !sc.RapidCommit || sc.Preference != 255 {
		t.Fatalf("updated %+v", sc)
	}

	var ex gen.Exclusion6
	a.must(201, "POST", fmt.Sprintf("/v6/scopes/%d/exclusions", sc.Id), gen.Range6{Start: "2001:db8:1::100", End: "2001:db8:1::10f"}).json(t, &ex)
	a.must(400, "POST", fmt.Sprintf("/v6/scopes/%d/exclusions", sc.Id), gen.Range6{Start: "2001:db8:2::1", End: "2001:db8:2::2"})
	var exs []gen.Exclusion6
	ro.must(200, "GET", fmt.Sprintf("/v6/scopes/%d/exclusions", sc.Id), nil).json(t, &exs)
	if len(exs) != 1 {
		t.Fatalf("exclusions %+v", exs)
	}
	a.must(204, "DELETE", fmt.Sprintf("/v6/scopes/%d/exclusions/%d", sc.Id, ex.Id), nil)

	var res gen.Reservation6
	a.must(201, "POST", "/v6/reservations", gen.Reservation6Input{ScopeId: &sc.Id, Duid: "00:03:00:01:02:00:00:00:00:aa", Ip: "2001:db8:1::50", Hostname: ptr("nas")}).json(t, &res)
	if res.Duid != "000300010200000000aa" || res.Iaid != -1 {
		t.Fatalf("reservation %+v", res)
	}
	a.must(400, "POST", "/v6/reservations", gen.Reservation6Input{ScopeId: &sc.Id, Duid: "zz", Ip: "2001:db8:1::51"})
	a.must(200, "PUT", fmt.Sprintf("/v6/reservations/%d", res.Id), gen.Reservation6Input{Duid: res.Duid, Iaid: ptr(int64(5)), Ip: "2001:db8:1::51"}).json(t, &res)
	if res.Iaid != 5 || res.Ip != "2001:db8:1::51" || res.ScopeId != sc.Id {
		t.Fatalf("updated reservation %+v", res)
	}

	var defs []gen.Option6Definition
	ro.must(200, "GET", "/v6/option-definitions", nil).json(t, &defs)
	if len(defs) < 10 || defs[0].Code != 21 {
		t.Fatalf("definitions %+v", defs)
	}
	var ov gen.Option6Value
	a.must(200, "PUT", "/v6/options/server/0/23", gen.Option6Input{Value: "2001:db8::53"}).json(t, &ov)
	if ov.Name != "dns-servers" || ov.Type != gen.Option6TypeIpv6List {
		t.Fatalf("option %+v", ov)
	}
	a.must(400, "PUT", "/v6/options/server/0/3", gen.Option6Input{Value: "x"})
	a.must(404, "PUT", "/v6/options/scope/999/23", gen.Option6Input{Value: "2001:db8::53"})
	a.must(200, "PUT", fmt.Sprintf("/v6/options/scope/%d/24", sc.Id), gen.Option6Input{Value: "lan.example"})
	var opts []gen.Option6Value
	ro.must(200, "GET", fmt.Sprintf("/v6/options/scope/%d", sc.Id), nil).json(t, &opts)
	if len(opts) != 1 || opts[0].Value != "lan.example" {
		t.Fatalf("scope options %+v", opts)
	}
	a.must(204, "DELETE", "/v6/options/server/0/23", nil)

	// Bindings.
	p := st.scopes6.Snapshot().ByID(sc.Id)
	l, ok, err := st.leases6.Assign(context.Background(), lease6.Request{Client: lease6.Client{DUID: "aabbcc", IAID: 1, Kind: lease6.KindNA}, Pool: p}, true)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, _, err := st.leases6.Assign(context.Background(), lease6.Request{Client: lease6.Client{DUID: "aabbcc", IAID: 2, Kind: lease6.KindPD}, Pool: p}, true); err != nil {
		t.Fatal(err)
	}
	var leases []gen.Lease6
	ro.must(200, "GET", "/v6/leases?q=aabbcc", nil).json(t, &leases)
	if len(leases) != 2 || leases[0].ScopeId != sc.Id {
		t.Fatalf("leases %+v", leases)
	}
	var stats []gen.Scope6Stats
	ro.must(200, "GET", "/v6/stats", nil).json(t, &stats)
	if len(stats) != 1 || stats[0].AddressesInUse != 1 || stats[0].PrefixesInUse != 1 || stats[0].Size != "256" || stats[0].PrefixCapacity != "65536" {
		t.Fatalf("stats %+v", stats)
	}
	ro.must(403, "DELETE", "/v6/leases/"+l.Addr().String(), nil)
	// The UI percent-encodes the colons of the address.
	a.must(204, "DELETE", "/v6/leases/"+strings.ReplaceAll(l.Addr().String(), ":", "%3A"), nil)
	a.must(404, "DELETE", "/v6/leases/"+l.Addr().String(), nil)

	// Deleting the scope removes its reservations.
	a.must(204, "DELETE", fmt.Sprintf("/v6/scopes/%d", sc.Id), nil)
	var all []gen.Reservation6
	a.must(200, "GET", "/v6/reservations", nil).json(t, &all)
	if len(all) != 0 {
		t.Fatalf("reservations left %+v", all)
	}
	var info gen.SystemInfo
	a.must(200, "GET", "/system", nil).json(t, &info)
	if info.Dhcp6Interfaces == nil {
		t.Fatal("dhcp6_interfaces missing")
	}
}

func TestIPv6ReadOnlyPartner(t *testing.T) {
	fh := &fakeHA{readOnly: true}
	st := newStackWith(t, func(d *Deps) { d.HA = fh })
	a := st.admin()
	a.must(409, "POST", "/v6/scopes", scope6Input("lan6", "2001:db8:1::/64"))
	a.must(409, "PUT", "/v6/options/server/0/23", gen.Option6Input{Value: "2001:db8::53"})
	a.must(200, "GET", "/v6/scopes", nil)
	a.must(404, "DELETE", "/v6/leases/2001:db8:1::5", nil) // lease operations stay possible
}

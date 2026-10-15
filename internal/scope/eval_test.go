package scope

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
)

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*", "", true},
		{"*", "anything", true},
		{"PXEClient*", "PXEClient:Arch:00007:UNDI:003016", true},
		{"pxeclient*", "PXEClient:Arch", true},
		{"MSFT 5.0", "MSFT 5.0", true},
		{"MSFT 5.0", "MSFT 5.01", false},
		{"00:15:5d:*", "00:15:5d:01:02:03", true},
		{"00:15:5d:*", "00:15:5e:01:02:03", false},
		{"??:15:*", "aa:15:00", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"*ab", "aab", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := Glob(c.p, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

func TestParseUserClass(t *testing.T) {
	if got := ParseUserClass([]byte("\x03abc\x02de")); !reflect.DeepEqual(got, []string{"abc", "de"}) {
		t.Errorf("RFC 3004 list: %v", got)
	}
	if got := ParseUserClass([]byte("RRAS.Microsoft")); !reflect.DeepEqual(got, []string{"RRAS.Microsoft"}) {
		t.Errorf("raw: %v", got)
	}
	if ParseUserClass(nil) != nil {
		t.Error("nil")
	}
}

func rg(a, b uint32) ip4.Range { return ip4.Range{Start: a, End: b} }

func TestSubtractRanges(t *testing.T) {
	r := rg(10, 100)
	cases := []struct {
		sub  []ip4.Range
		want []ip4.Range
	}{
		{nil, []ip4.Range{rg(10, 100)}},
		{[]ip4.Range{rg(20, 30)}, []ip4.Range{rg(10, 19), rg(31, 100)}},
		{[]ip4.Range{rg(50, 60), rg(20, 30)}, []ip4.Range{rg(10, 19), rg(31, 49), rg(61, 100)}},
		{[]ip4.Range{rg(10, 100)}, []ip4.Range{}},
		{[]ip4.Range{rg(0, 15), rg(95, 200)}, []ip4.Range{rg(16, 94)}},
		{[]ip4.Range{rg(200, 300)}, []ip4.Range{rg(10, 100)}},
	}
	for _, c := range cases {
		if got := subtractRanges(r, c.sub); !reflect.DeepEqual(got, c.want) {
			t.Errorf("subtract %v = %v, want %v", c.sub, got, c.want)
		}
	}
}

func mustSet(t *testing.T, s *Store, o Owner, code uint8, value string) {
	t.Helper()
	if _, err := s.SetOption(context.Background(), OptionValue{Owner: o, Code: code, Value: value}); err != nil {
		t.Fatalf("set option %d=%s on %v: %v", code, value, o, err)
	}
}

func mustPolicy(t *testing.T, s *Store, p Policy) Policy {
	t.Helper()
	p.Enabled = true
	out, err := s.CreatePolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("create policy %s: %v", p.Name, err)
	}
	return out
}

func dur(d time.Duration) *time.Duration { return &d }

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// TestOptionResolution is the table driven check of the inheritance chain
// server < scope < server policy < scope policy < reservation.
func TestOptionResolution(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	if err != nil {
		t.Fatal(err)
	}
	scopeOwner := Owner{Kind: OwnerScope, ID: sc.ID}

	mustSet(t, s, ServerOwner, 6, "1.1.1.1")
	mustSet(t, s, ServerOwner, 15, "srv.example")
	mustSet(t, s, ServerOwner, 42, "10.0.0.123")
	mustSet(t, s, scopeOwner, 6, "10.0.0.53")
	mustSet(t, s, scopeOwner, 3, "10.0.0.1")

	if _, err := s.CreateClass(ctx, Class{Name: "PXE", Type: ClassVendor, Data: "PXEClient*"}); err != nil {
		t.Fatal(err)
	}
	win := mustPolicy(t, s, Policy{Name: "windows", Conditions: []Condition{{Field: FieldVendorClass, Value: "MSFT*"}}})
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: win.ID}, 15, "win.example")

	lab := mustPolicy(t, s, Policy{ScopeID: sc.ID, Name: "lab-macs", Order: 1,
		Conditions: []Condition{{Field: FieldMAC, Value: "02:00:00:00:00:*"}}, LeaseTime: dur(10 * time.Minute)})
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: lab.ID}, 6, "10.0.0.54")

	pxe := mustPolicy(t, s, Policy{ScopeID: sc.ID, Name: "pxe", Order: 2,
		Conditions: []Condition{{Field: FieldClass, Value: "PXE"}}})
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: pxe.ID}, 6, "10.0.0.55")
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: pxe.ID}, 66, "10.0.0.5")
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: pxe.ID}, 67, "pxelinux.0")

	// OR policy with a negated condition; disabled policy must be ignored.
	or := mustPolicy(t, s, Policy{ScopeID: sc.ID, Name: "or", Order: 3, Operator: CombineOr,
		Conditions: []Condition{{Field: FieldHostname, Value: "kiosk-*"}, {Field: FieldCircuitID, Value: "0x0107*"}}})
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: or.ID}, 252, "http://wpad/kiosk.pac")
	off := mustPolicy(t, s, Policy{ScopeID: sc.ID, Name: "off", Order: 4,
		Conditions: []Condition{{Field: FieldMAC, Operator: OpNotEqual, Value: "ff:ff:ff:ff:ff:ff"}}})
	mustSet(t, s, Owner{Kind: OwnerPolicy, ID: off.ID}, 26, "1400")
	off.Enabled = false
	if _, err := s.UpdatePolicy(ctx, off); err != nil {
		t.Fatal(err)
	}

	res, err := s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, IP: addr("10.0.0.20"), MAC: "02:00:00:00:00:99"})
	if err != nil {
		t.Fatal(err)
	}
	mustSet(t, s, Owner{Kind: OwnerReservation, ID: res.ID}, 6, "10.0.0.99")

	snap := s.Snapshot()
	pool := snap.ByID(sc.ID)
	cases := []struct {
		name      string
		ci        ClientInfo
		want      map[uint8]string
		absent    []uint8
		policies  []string
		leaseTime time.Duration
	}{
		{"plain", ClientInfo{MAC: "aa:00:00:00:00:01"},
			map[uint8]string{6: "10.0.0.53", 15: "srv.example", 42: "10.0.0.123", 3: "10.0.0.1"},
			[]uint8{66, 67, 252, 26}, nil, time.Hour},
		{"server policy vendor class", ClientInfo{MAC: "aa:00:00:00:00:01", VendorClass: "MSFT 5.0"},
			map[uint8]string{15: "win.example", 6: "10.0.0.53"}, nil, []string{"windows"}, time.Hour},
		{"scope policy beats scope", ClientInfo{MAC: "02:00:00:00:00:05"},
			map[uint8]string{6: "10.0.0.54"}, []uint8{66}, []string{"lab-macs"}, 10 * time.Minute},
		{"lower processing order wins", ClientInfo{MAC: "02:00:00:00:00:05", VendorClass: "PXEClient:Arch:00000"},
			map[uint8]string{6: "10.0.0.54", 66: "10.0.0.5", 67: "pxelinux.0"}, nil, []string{"lab-macs", "pxe"}, 10 * time.Minute},
		{"pxe class", ClientInfo{MAC: "aa:00:00:00:00:02", VendorClass: "PXEClient:Arch:00007:UNDI:003016"},
			map[uint8]string{6: "10.0.0.55", 66: "10.0.0.5", 67: "pxelinux.0", 15: "srv.example"}, nil, []string{"pxe"}, time.Hour},
		{"or by hostname", ClientInfo{MAC: "aa:00:00:00:00:03", Hostname: "Kiosk-12"},
			map[uint8]string{252: "http://wpad/kiosk.pac"}, nil, []string{"or"}, time.Hour},
		{"or by circuit id hex", ClientInfo{MAC: "aa:00:00:00:00:04", CircuitID: []byte{0x01, 0x07, 0x33}},
			map[uint8]string{252: "http://wpad/kiosk.pac"}, nil, []string{"or"}, time.Hour},
		{"reservation wins", ClientInfo{MAC: "02:00:00:00:00:99"},
			map[uint8]string{6: "10.0.0.99", 3: "10.0.0.1"}, nil, []string{"lab-macs"}, 10 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := snap.Evaluate(pool, &c.ci)
			if !reflect.DeepEqual(ev.PolicyNames(), c.policies) {
				t.Errorf("matched %v, want %v", ev.PolicyNames(), c.policies)
			}
			if ev.LeaseTime != c.leaseTime {
				t.Errorf("lease time %v, want %v", ev.LeaseTime, c.leaseTime)
			}
			var r *Reservation
			if rr := pool.ReservationFor(c.ci.MAC, ""); rr != nil {
				r = rr
			}
			opts := snap.Options(&ev, r)
			for code, want := range c.want {
				def, _ := dhcpopt.Builtin(code)
				_, wire, _ := dhcpopt.Parse(def.Type, want)
				if string(opts[code]) != string(wire) {
					t.Errorf("option %d = % x, want %s", code, opts[code], want)
				}
			}
			for _, code := range c.absent {
				if _, ok := opts[code]; ok {
					t.Errorf("option %d unexpectedly present", code)
				}
			}
		})
	}
}

func TestPolicyRanges(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, err := s.CreateScope(ctx, lan("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	if err != nil {
		t.Fatal(err)
	}
	mustPolicy(t, s, Policy{ScopeID: sc.ID, Name: "phones", Conditions: []Condition{{Field: FieldVendorClass, Value: "Polycom*"}},
		Ranges: []AddrRange{{addr("10.0.0.150"), addr("10.0.0.159")}}})
	pool := s.Snapshot().ByID(sc.ID)

	phone := s.Snapshot().Evaluate(pool, &ClientInfo{MAC: "aa:00:00:00:00:01", VendorClass: "Polycom-VVX"})
	if !reflect.DeepEqual(phone.Ranges, []ip4.Range{{Start: ip4.MustFromAddr(addr("10.0.0.150")), End: ip4.MustFromAddr(addr("10.0.0.159"))}}) {
		t.Fatalf("phone ranges %v", phone.Ranges)
	}
	other := s.Snapshot().Evaluate(pool, &ClientInfo{MAC: "aa:00:00:00:00:02"})
	if other.Allowed(ip4.MustFromAddr(addr("10.0.0.155"))) || !other.Allowed(ip4.MustFromAddr(addr("10.0.0.149"))) || !other.Allowed(ip4.MustFromAddr(addr("10.0.0.160"))) {
		t.Fatalf("general ranges %v", other.Ranges)
	}

	// Validation of ranges.
	bad := []Policy{
		{ScopeID: sc.ID, Name: "outside", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}, Ranges: []AddrRange{{addr("10.0.0.50"), addr("10.0.0.60")}}},
		{ScopeID: sc.ID, Name: "overlap", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}, Ranges: []AddrRange{{addr("10.0.0.155"), addr("10.0.0.165")}}},
		{Name: "server-range", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}, Ranges: []AddrRange{{addr("10.0.0.120"), addr("10.0.0.121")}}},
	}
	for _, p := range bad {
		if _, err := s.CreatePolicy(ctx, p); err == nil {
			t.Errorf("policy %s accepted", p.Name)
		}
	}
	// The scope range cannot shrink below a policy range.
	sc.End = addr("10.0.0.152")
	if _, err := s.UpdateScope(ctx, sc); !isValidation(err) {
		t.Fatalf("scope shrink accepted: %v", err)
	}
}

func TestPolicyValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cases := map[string]Policy{
		"no name":       {Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}},
		"no conditions": {Name: "x"},
		"bad field":     {Name: "x", Conditions: []Condition{{Field: "colour", Value: "red"}}},
		"bad operator":  {Name: "x", Conditions: []Condition{{Field: FieldMAC, Operator: "like", Value: "aa:*"}}},
		"bad mac":       {Name: "x", Conditions: []Condition{{Field: FieldMAC, Value: "zz:*"}}},
		"short lease":   {Name: "x", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}, LeaseTime: dur(time.Second)},
		"combine":       {Name: "x", Operator: "xor", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}},
		"missing class": {Name: "x", Conditions: []Condition{{Field: FieldClass, Value: "nope"}}},
		"empty value":   {Name: "x", Conditions: []Condition{{Field: FieldHostname, Value: " "}}},
		"unknown scope": {Name: "x", ScopeID: 42, Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreatePolicy(ctx, p); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	p := mustPolicy(t, s, Policy{Name: "dup", Conditions: []Condition{{Field: FieldMAC, Value: "AA-BB-CC-DD-EE-FF"}}})
	if p.Conditions[0].Value != "aa:bb:cc:dd:ee:ff" || p.Order != 1 {
		t.Fatalf("not normalised: %+v", p)
	}
	if _, err := s.CreatePolicy(ctx, Policy{Name: "dup", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	p2 := mustPolicy(t, s, Policy{Name: "second", Conditions: []Condition{{Field: FieldMAC, Value: "aa:*"}}})
	if p2.Order != 2 {
		t.Fatalf("auto order = %d", p2.Order)
	}
	if err := s.DeletePolicy(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPolicy(ctx, p.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("policy not deleted")
	}
}

func TestClassesInUse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c, err := s.CreateClass(ctx, Class{Name: "Phones", Type: ClassVendor, Data: "Polycom*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateClass(ctx, Class{Name: "phones", Type: ClassUser, Data: "x"}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("case-insensitive duplicate accepted: %v", err)
	}
	p := mustPolicy(t, s, Policy{Name: "p", Conditions: []Condition{{Field: FieldClass, Value: "Phones"}}})
	if err := s.DeleteClass(ctx, c.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("class in use deleted: %v", err)
	}
	c.Name = "VoIP"
	if _, err := s.UpdateClass(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPolicy(ctx, p.ID)
	if got.Conditions[0].Value != "VoIP" {
		t.Fatalf("rename not propagated: %+v", got.Conditions)
	}
	if !s.Snapshot().policyMatches(s.Snapshot().serverPolicies[0], &ClientInfo{VendorClass: "Polycom-VVX"}) {
		t.Fatal("renamed class does not match")
	}
}

func TestOptionValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cases := map[string]OptionValue{
		"managed 1":      {Owner: ServerOwner, Code: 1, Value: "255.255.255.0"},
		"managed 51":     {Owner: ServerOwner, Code: 51, Value: "3600"},
		"managed 54":     {Owner: ServerOwner, Code: 54, Value: "10.0.0.1"},
		"undefined":      {Owner: ServerOwner, Code: 224, Value: "x"},
		"wrong type":     {Owner: ServerOwner, Code: 6, Type: dhcpopt.TypeString, Value: "x"},
		"bad value":      {Owner: ServerOwner, Code: 6, Value: "dns.example"},
		"missing owner":  {Owner: Owner{Kind: OwnerScope, ID: 99}, Code: 6, Value: "1.1.1.1"},
		"bad owner kind": {Owner: Owner{Kind: "class", ID: 1}, Code: 6, Value: "1.1.1.1"},
	}
	for name, ov := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.SetOption(ctx, ov); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Custom definitions.
	if _, err := s.CreateOptionDefinition(ctx, dhcpopt.Definition{Code: 6, Name: "x", Type: dhcpopt.TypeHex}); err == nil {
		t.Fatal("redefinition of built-in accepted")
	}
	if _, err := s.CreateOptionDefinition(ctx, dhcpopt.Definition{Code: 224, Name: "site-local", Type: dhcpopt.TypeString}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.SetOption(ctx, OptionValue{Owner: ServerOwner, Code: 224, Value: "hello"})
	if err != nil || ov.Type != dhcpopt.TypeString {
		t.Fatalf("custom option: %+v %v", ov, err)
	}
	if err := s.DeleteOptionDefinition(ctx, 224); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("definition with values deleted: %v", err)
	}
	if string(s.Snapshot().serverOptions[224]) != "hello" {
		t.Fatal("custom option not in snapshot")
	}
	// Set replaces; delete removes.
	mustSet(t, s, ServerOwner, 6, "1.1.1.1, 8.8.8.8")
	mustSet(t, s, ServerOwner, 6, "9.9.9.9")
	opts, _ := s.ListOptions(ctx, ServerOwner)
	if len(opts) != 2 || opts[0].Code != 6 || opts[0].Value != "9.9.9.9" {
		t.Fatalf("options %+v", opts)
	}
	if err := s.DeleteOption(ctx, ServerOwner, 6); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOption(ctx, ServerOwner, 6); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("double delete")
	}
	if _, ok := s.Snapshot().serverOptions[6]; ok {
		t.Fatal("deleted option still in snapshot")
	}
	defs, _ := s.ListOptionDefinitions(ctx)
	found := false
	for _, d := range defs {
		if d.Code == 224 && !d.Builtin {
			found = true
		}
	}
	if !found {
		t.Fatal("custom definition not listed")
	}
}

func TestOptionsCascadeWithOwner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sc, _ := s.CreateScope(ctx, lan("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	mustSet(t, s, Owner{Kind: OwnerScope, ID: sc.ID}, 3, "10.0.0.1")
	if err := s.DeleteScope(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	opts, _ := s.queryOptions(ctx, "")
	if len(opts) != 0 {
		t.Fatalf("options not cascaded: %+v", opts)
	}
}

func TestMACFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.AddFilter(ctx, MACFilter{List: FilterDeny, Pattern: "AA-BB-CC-*"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFilter(ctx, MACFilter{List: FilterDeny, Pattern: "aa:bb:cc:*"}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate filter: %v", err)
	}
	if _, err := s.AddFilter(ctx, MACFilter{List: "grey", Pattern: "aa:*"}); err == nil {
		t.Fatal("bad list accepted")
	}
	if _, err := s.AddFilter(ctx, MACFilter{List: FilterAllow, Pattern: "nope"}); err == nil {
		t.Fatal("bad pattern accepted")
	}
	allow, err := s.AddFilter(ctx, MACFilter{List: FilterAllow, Pattern: "02:00:00:00:00:01"})
	if err != nil {
		t.Fatal(err)
	}
	// Lists are inactive until enabled.
	if f, _ := s.Snapshot().Filtered("aa:bb:cc:00:00:01"); f {
		t.Fatal("filter active while disabled")
	}
	if err := s.SetFilterSettings(ctx, FilterSettings{DenyEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Snapshot().Filtered("aa:bb:cc:00:00:01"); !f {
		t.Fatal("deny list not applied")
	}
	if f, _ := s.Snapshot().Filtered("02:00:00:00:00:07"); f {
		t.Fatal("unlisted MAC denied")
	}
	if err := s.SetFilterSettings(ctx, FilterSettings{DenyEnabled: true, AllowEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Snapshot().Filtered("02:00:00:00:00:07"); !f {
		t.Fatal("allow list not applied")
	}
	if f, _ := s.Snapshot().Filtered("02:00:00:00:00:01"); f {
		t.Fatal("allowed MAC denied")
	}
	cfg, _ := s.FilterSettings(ctx)
	if !cfg.AllowEnabled || !cfg.DenyEnabled {
		t.Fatalf("settings %+v", cfg)
	}
	if err := s.DeleteFilter(ctx, allow.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListFilters(ctx, "")
	if len(all) != 1 {
		t.Fatalf("filters %+v", all)
	}
}

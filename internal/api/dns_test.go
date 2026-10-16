package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/dnsupdate"
)

type fakeRegs []dnsupdate.Registration

func (f fakeRegs) Registrations() []dnsupdate.Registration { return f }

const testSecret = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestDNSKeysZonesAndScopeSettings(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	op := st.login("op", "operator-pass")

	// Keys: admin only, secret validated and never returned.
	op.must(403, "POST", "/dns/keys", gen.DnsKeyInput{Name: "k", Secret: ptr(testSecret)})
	a.must(400, "POST", "/dns/keys", gen.DnsKeyInput{Name: "k", Secret: ptr("c2hvcnQ=")})
	a.must(400, "POST", "/dns/keys", gen.DnsKeyInput{Name: "k"})
	var key gen.DnsKey
	r := a.must(201, "POST", "/dns/keys", gen.DnsKeyInput{Name: "Leasorix-Key.", Secret: ptr(testSecret)})
	r.json(t, &key)
	if key.Name != "leasorix-key" || key.Algorithm != "hmac-sha256" || strings.Contains(string(r.body), testSecret) {
		t.Fatalf("key %s", r.body)
	}
	alg := gen.HmacSha512
	a.must(200, "PUT", fmt.Sprintf("/dns/keys/%d", key.Id), gen.DnsKeyInput{Name: "leasorix-key", Algorithm: &alg}).json(t, &key)
	if key.Algorithm != "hmac-sha512" {
		t.Fatalf("updated key %+v", key)
	}
	if k, err := st.scopes.GetDNSKey(t.Context(), key.Id); err != nil || k.Secret != testSecret {
		t.Fatalf("secret not kept on update: %+v %v", k, err)
	}
	if r := op.must(200, "GET", "/dns/keys", nil); strings.Contains(string(r.body), testSecret) {
		t.Fatal("secret listed")
	}

	// Zones: operators may manage them.
	var zone gen.DnsZone
	op.must(201, "POST", "/dns/zones", gen.DnsZoneInput{Name: "Example.LAN", Server: "192.0.2.53", KeyId: &key.Id}).json(t, &zone)
	if zone.Name != "example.lan" || zone.KeyName != "leasorix-key" {
		t.Fatalf("zone %+v", zone)
	}
	op.must(409, "POST", "/dns/zones", gen.DnsZoneInput{Name: "example.lan", Server: "192.0.2.53"})
	op.must(400, "POST", "/dns/zones", gen.DnsZoneInput{Name: "bad zone", Server: "192.0.2.53"})
	op.must(400, "POST", "/dns/zones", gen.DnsZoneInput{Name: "x.example", Server: "192.0.2.53:99999"})
	op.must(400, "POST", "/dns/zones", gen.DnsZoneInput{Name: "x.example", Server: "192.0.2.53", KeyId: ptr(int64(42))})
	op.must(201, "POST", "/dns/zones", gen.DnsZoneInput{Name: "2.0.192.in-addr.arpa", Server: "ns1.example.lan:5353"})
	a.must(409, "DELETE", fmt.Sprintf("/dns/keys/%d", key.Id), nil) // in use
	op.must(200, "PUT", fmt.Sprintf("/dns/zones/%d", zone.Id), gen.DnsZoneInput{Name: "example.lan", Server: "192.0.2.54"}).json(t, &zone)
	if zone.KeyId != 0 || zone.Server != "192.0.2.54" {
		t.Fatalf("zone update %+v", zone)
	}
	a.must(204, "DELETE", fmt.Sprintf("/dns/keys/%d", key.Id), nil)
	var zones []gen.DnsZone
	op.must(200, "GET", "/dns/zones", nil).json(t, &zones)
	if len(zones) != 2 {
		t.Fatalf("zones %+v", zones)
	}
	op.must(204, "DELETE", fmt.Sprintf("/dns/zones/%d", zone.Id), nil)

	// Scope DNS settings: defaults, update, absent keeps them.
	var sc gen.Scope
	op.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	if sc.Dns.Mode != gen.DnsModeNever || !*sc.Dns.Cleanup || !*sc.Dns.Protect {
		t.Fatalf("default dns settings %+v", sc.Dns)
	}
	in := scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")
	in.Dns = &gen.DnsSettings{Mode: gen.DnsModeClient, Domain: ptr("Lan.Example."), Ttl: ptr(600), Protect: ptr(false)}
	op.must(200, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), in).json(t, &sc)
	if sc.Dns.Mode != gen.DnsModeClient || *sc.Dns.Domain != "lan.example" || *sc.Dns.Ttl != 600 || *sc.Dns.Protect || !*sc.Dns.Cleanup {
		t.Fatalf("dns settings %+v", sc.Dns)
	}
	op.must(200, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	if sc.Dns.Mode != gen.DnsModeClient {
		t.Fatal("absent dns settings were reset")
	}
	in.Dns = &gen.DnsSettings{Mode: "sometimes"}
	op.must(400, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), in)
	in.Dns = &gen.DnsSettings{Mode: gen.DnsModeAlways, Ttl: ptr(-1)}
	op.must(400, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), in)

	// Registrations without an updater: empty list.
	var regs []gen.DnsRegistration
	op.must(200, "GET", "/dns/registrations", nil).json(t, &regs)
	if len(regs) != 0 {
		t.Fatalf("registrations %+v", regs)
	}
}

func TestDNSRegistrations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	st := newStackWith(t, func(d *Deps) {
		d.DNS = fakeRegs{
			{IP: 0x0a000005, WantFQDN: "pc5.lan.example", WantFwd: true, WantRev: true, WantDHCID: "x", HaveFwd: "pc5.lan.example", HaveDHCID: "x", HaveRev: "pc5.lan.example", Updated: now},
			{IP: 0x0a000006, WantFQDN: "pc6.lan.example", WantFwd: true, Conflict: true, LastError: "in use"},
			{IP: 0x0a000007, HaveRev: "old.lan.example", Attempts: 2, NextAttempt: now, LastError: "timeout"},
		}
	})
	a := st.admin()
	var regs []gen.DnsRegistration
	a.must(200, "GET", "/dns/registrations", nil).json(t, &regs)
	if len(regs) != 3 || regs[0].Ip != "10.0.0.5" || regs[0].State != "registered" || !*regs[0].Protected ||
		regs[1].State != "conflict" || regs[2].State != "error" || regs[2].Fqdn != "" || regs[2].PtrRecord != "old.lan.example" {
		t.Fatalf("registrations %+v", regs)
	}
}

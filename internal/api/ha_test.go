package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
)

type fakeHA struct {
	readOnly bool
	down     bool
	err      error
}

func (f *fakeHA) Status() ha.Status {
	st := "COMMUNICATION_INTERRUPTED"
	if f.down {
		st = "PARTNER_DOWN"
	}
	return ha.Status{NodeID: "dhcp-b", PeerID: "dhcp-a", PeerAddress: "10.0.0.1:8068", Mode: "hot-standby", Role: "secondary",
		Replication: "sync", TLSMode: "mtls", State: st, ConfigReadOnly: f.readOnly, ConfigLeader: !f.readOnly, MCLT: time.Hour}
}

func (f *fakeHA) PartnerDown(context.Context) error {
	if f.err != nil {
		return f.err
	}
	f.down = true
	return nil
}

func (f *fakeHA) ConfigReadOnly() bool { return f.readOnly }

func TestHAReadOnlyPartner(t *testing.T) {
	fh := &fakeHA{}
	st := newStackWith(t, func(d *Deps) { d.HA = fh })
	a := st.admin()
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	ctx := context.Background()
	c := lease.Client{MAC: "02:00:00:00:00:01"}
	p := st.scopes.Snapshot().ByID(sc.Id)
	o := st.leases.Offer(ctx, c, []lease.Candidate{lease.PoolCandidate(p)}, 0)
	if _, err := st.leases.Request(ctx, c, lease.PoolCandidate(p), o.IP); err != nil {
		t.Fatal(err)
	}

	fh.readOnly = true
	var info gen.SystemInfo
	a.must(200, "GET", "/system", nil).json(t, &info)
	if !info.HaEnabled || !info.ConfigReadOnly || info.ConfigLeader == nil || *info.ConfigLeader != "dhcp-a" {
		t.Fatalf("system info %+v", info)
	}
	r := a.do("POST", "/scopes", scopeInput("other", "10.1.0.0/24", "10.1.0.100", "10.1.0.200"))
	if r.code != 409 || !strings.Contains(string(r.body), "dhcp-a") {
		t.Fatalf("config change on read-only partner: %d %s", r.code, r.body)
	}
	a.must(409, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), scopeInput("x", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	a.must(409, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	a.must(409, "POST", "/leases/"+ip4.String(o.IP)+"/reservation", map[string]any{})
	// Reading and lease operations stay possible.
	a.must(200, "GET", "/scopes", nil)
	a.must(204, "DELETE", "/leases/"+ip4.String(o.IP), nil)
	a.must(204, "POST", "/auth/logout", nil)
}

func TestHAPartnerDownAndStatus(t *testing.T) {
	fh := &fakeHA{}
	st := newStackWith(t, func(d *Deps) { d.HA = fh })
	a := st.admin()
	var s gen.HAStatus
	a.must(200, "GET", "/ha/status", nil).json(t, &s)
	if !s.Enabled || *s.State != "COMMUNICATION_INTERRUPTED" || *s.Mclt != 3600 || *s.PeerId != "dhcp-a" {
		t.Fatalf("status %+v", s)
	}
	a.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	op := st.login("op", "operator-pass")
	op.must(403, "POST", "/ha/partner-down", nil)
	a.must(200, "POST", "/ha/partner-down", nil).json(t, &s)
	if *s.State != "PARTNER_DOWN" {
		t.Fatalf("state %s", *s.State)
	}
	fh.err = ha.ErrPartnerReachable
	a.must(409, "POST", "/ha/partner-down", nil)

	// Without HA.
	st2 := newStack(t)
	a2 := st2.admin()
	a2.must(200, "GET", "/ha/status", nil).json(t, &s)
	if s.Enabled {
		t.Fatal("HA reported as enabled")
	}
	a2.must(409, "POST", "/ha/partner-down", nil)
}

func TestHACertificateGeneration(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var certs gen.HACertificates
	a.must(200, "POST", "/ha/certificates", map[string]any{"nodes": []string{"dhcp-a", "dhcp-b"}}).json(t, &certs)
	if !strings.Contains(certs.CaCert, "BEGIN CERTIFICATE") || len(certs.Nodes) != 2 || !strings.Contains(certs.Nodes[1].Key, "PRIVATE KEY") {
		t.Fatalf("certificates %+v", certs)
	}
	a.must(400, "POST", "/ha/certificates", map[string]any{"nodes": []string{"a", "a"}})
	a.must(400, "POST", "/ha/certificates", map[string]any{"nodes": []string{"bad id!", "b"}})
}

func TestScopeExportImportAndSplit(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.5.0.0/24", "10.5.0.1", "10.5.0.100")).json(t, &sc)
	a.must(200, "PUT", fmt.Sprintf("/options/scope/%d/6", sc.Id), map[string]any{"value": "10.5.0.53"})

	var bundle map[string]any
	a.must(200, "GET", fmt.Sprintf("/scopes/%d/export", sc.Id), nil).json(t, &bundle)
	if bundle["format"] != "leasorix-scope/v1" {
		t.Fatalf("bundle %v", bundle)
	}
	// Import on a second server.
	st2 := newStack(t)
	a2 := st2.admin()
	var imported gen.Scope
	a2.must(201, "POST", "/scopes/import", bundle).json(t, &imported)
	if imported.Name != "lan" || imported.Subnet != "10.5.0.0/24" {
		t.Fatalf("imported %+v", imported)
	}
	a2.must(409, "POST", "/scopes/import", bundle)
	bundle["format"] = "other"
	a2.must(400, "POST", "/scopes/import", bundle)

	// Split 80/20 on the first server.
	var split struct {
		LocalRange      gen.BundleRange `json:"local_range"`
		PartnerRange    gen.BundleRange `json:"partner_range"`
		ExclusionsAdded []gen.Exclusion `json:"exclusions_added"`
		Partner         json.RawMessage `json:"partner"`
	}
	a.must(200, "POST", fmt.Sprintf("/scopes/%d/split", sc.Id), gen.SplitScopeInput{LocalPercent: 80}).json(t, &split)
	if split.LocalRange.End != "10.5.0.80" || split.PartnerRange.Start != "10.5.0.81" || len(split.ExclusionsAdded) != 1 {
		t.Fatalf("split %+v", split)
	}
	a.must(400, "POST", fmt.Sprintf("/scopes/%d/split", sc.Id), gen.SplitScopeInput{LocalPercent: 100})
	st3 := newStack(t)
	a3 := st3.admin()
	var partner map[string]any
	_ = json.Unmarshal(split.Partner, &partner)
	a3.must(201, "POST", "/scopes/import", partner)
	p := st3.scopes.Snapshot().Pools()[0]
	if p.Dynamic(ip4.MustFromAddr(netip.MustParseAddr("10.5.0.80"))) || !p.Dynamic(ip4.MustFromAddr(netip.MustParseAddr("10.5.0.81"))) {
		t.Fatal("partner scope does not exclude the local part")
	}
}

func TestLeaseExportImport(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.6.0.0/24", "10.6.0.10", "10.6.0.200")).json(t, &sc)
	ctx := context.Background()
	p := st.scopes.Snapshot().ByID(sc.Id)
	for i := range 5 {
		c := lease.Client{MAC: fmt.Sprintf("02:00:00:00:00:%02x", i)}
		o := st.leases.Offer(ctx, c, []lease.Candidate{lease.PoolCandidate(p)}, 0)
		if _, err := st.leases.Request(ctx, c, lease.PoolCandidate(p), o.IP); err != nil {
			t.Fatal(err)
		}
	}
	r := a.must(200, "GET", "/leases/export", nil)
	if !strings.Contains(r.hdr.Get("Content-Disposition"), "attachment") {
		t.Fatal("export is not offered as download")
	}
	var export map[string]any
	r.json(t, &export)

	st2 := newStack(t)
	a2 := st2.admin()
	a2.must(201, "POST", "/scopes", scopeInput("lan", "10.6.0.0/24", "10.6.0.10", "10.6.0.200"))
	var res gen.LeaseImportResult
	a2.must(200, "POST", "/leases/import", export).json(t, &res)
	if res.Records != 5 || res.Changed != 5 || res.Conflicts != 0 {
		t.Fatalf("import %+v", res)
	}
	if len(st2.leases.All()) != 5 {
		t.Fatal("leases not imported")
	}
	a2.must(200, "POST", "/leases/import", export).json(t, &res)
	if res.Changed != 0 {
		t.Fatalf("re-import changed %d records", res.Changed)
	}
	export["format"] = "x"
	a2.must(400, "POST", "/leases/import", export)
	a2.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	st2.login("op", "operator-pass").must(403, "POST", "/leases/import", export)
}

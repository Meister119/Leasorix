package dnsupdate

import (
	"bytes"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/scope"
)

// RFC 4701 section 3.6.
func TestDHCIDVectors(t *testing.T) {
	for _, tc := range []struct{ name, mac, cid, fqdn, want string }{
		{"duid via RFC 4361 client id", "", "ff00000001" + "00010006412df166010203040506", "chi6.example.com", "AAIBY2/AuCccgoJbsaxcQc9TUapptP69lOjxfNuVAA2kjEA="},
		{"client identifier", "", "010708090a0b0c", "chi.example.com", "AAEBOSD+XR3Os/0LozeXVqcNc7FwCfQdWL3b/NaiUDlW2No="},
		{"hardware address", "01:02:03:04:05:06", "", "client.example.com.", "AAABxLmlskllE0MVjd57zHcWmEH3pCQ6VytcKD//7es/deY="},
	} {
		if got := DHCID(tc.mac, tc.cid, tc.fqdn); got != tc.want {
			t.Errorf("%s: DHCID = %s, want %s", tc.name, got, tc.want)
		}
	}
	if DHCID("01:02:03:04:05:06", "", "Client.Example.COM") != DHCID("01:02:03:04:05:06", "", "client.example.com") {
		t.Error("DHCID must use the canonical (lower case) name")
	}
}

func wire(flags byte, labels ...string) []byte {
	b := []byte{flags, 0, 0}
	for _, l := range labels {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

func TestParseClientFQDN(t *testing.T) {
	f, err := ParseClientFQDN(wire(flagS|flagE, "PC1", "corp", "example"))
	if err != nil || !f.S || f.N || !f.E || f.Name != "pc1.corp.example" {
		t.Fatalf("wire: %+v %v", f, err)
	}
	// Partial name (no root label).
	f, err = ParseClientFQDN([]byte{flagE, 0, 0, 3, 'p', 'c', '2'})
	if err != nil || f.Name != "pc2" {
		t.Fatalf("partial: %+v %v", f, err)
	}
	// ASCII encoding (udhcpc -F).
	f, err = ParseClientFQDN(append([]byte{flagS, 0, 0}, "laptop."...))
	if err != nil || f.E || f.Name != "laptop" {
		t.Fatalf("ascii: %+v %v", f, err)
	}
	if _, err := ParseClientFQDN([]byte{flagE, 0, 0, 9, 'x'}); err == nil {
		t.Fatal("truncated label accepted")
	}
	if _, err := ParseClientFQDN([]byte{0}); err == nil {
		t.Fatal("short option accepted")
	}
}

func TestHostLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Laptop-01":                    "laptop-01",
		"pc.example.com":               "pc",
		"Marias iPhone":                "marias-iphone",
		"_under_score_":                "under-score",
		"---":                          "",
		"":                             "",
		"äöü":                          "",
		"x" + string(make([]byte, 70)): "x",
	} {
		if got := HostLabel(in); got != want {
			t.Errorf("HostLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecide(t *testing.T) {
	client := scope.DNSSettings{Mode: scope.DNSClient, Domain: "lan.example"}
	always := scope.DNSSettings{Mode: scope.DNSAlways}
	never := scope.DNSSettings{Mode: scope.DNSNever, Domain: "lan.example"}
	for _, tc := range []struct {
		name     string
		s        scope.DNSSettings
		opt      []byte
		host     string
		optDom   string
		fqdn     string
		fwd, rev bool
		flags    int // -1 = no reply
	}{
		{"client S=1", client, wire(flagS|flagE, "pc1"), "", "", "pc1.lan.example", true, true, flagS | flagE},
		{"client S=0: PTR only", client, wire(flagE, "pc1"), "", "", "pc1.lan.example", false, true, flagE},
		{"client N=1", client, wire(flagN|flagE, "pc1"), "", "", "pc1.lan.example", false, false, flagN | flagE},
		{"client without option", client, nil, "pc1", "", "pc1.lan.example", false, false, -1},
		{"client FQDN outside domain is re-qualified", client, wire(flagS|flagE, "www", "bank", "com"), "", "", "www.lan.example", true, true, flagS | flagE},
		{"always overrides S=0", always, wire(flagE, "pc1"), "", "Corp.Example.", "pc1.corp.example", true, true, flagS | flagO | flagE},
		{"always with host name only", always, nil, "Printer", "corp.example", "printer.corp.example", true, true, -1},
		{"always without domain", always, nil, "printer", "", "", false, false, -1},
		{"never answers N", never, wire(flagS|flagE, "pc1"), "", "", "pc1.lan.example", false, false, flagN | flagO | flagE},
		{"no usable name", always, wire(flagS|flagE, "---"), "", "corp.example", "", false, false, flagN | flagO | flagE},
	} {
		d := Decide(tc.s, tc.opt, tc.host, tc.optDom)
		if d.FQDN != tc.fqdn || d.Forward != tc.fwd || d.Reverse != tc.rev {
			t.Errorf("%s: got %+v", tc.name, d)
			continue
		}
		if tc.flags < 0 {
			if d.Reply != nil {
				t.Errorf("%s: unexpected reply %x", tc.name, d.Reply)
			}
			continue
		}
		if len(d.Reply) < 3 || int(d.Reply[0]) != tc.flags || d.Reply[1] != 255 || d.Reply[2] != 255 {
			t.Errorf("%s: reply %x, want flags %#x", tc.name, d.Reply, tc.flags)
			continue
		}
		if tc.fqdn != "" {
			back, err := ParseClientFQDN(d.Reply)
			if err != nil || back.Name != tc.fqdn {
				t.Errorf("%s: reply name %+v %v", tc.name, back, err)
			}
		}
	}
	// ASCII request gets an ASCII answer.
	d := Decide(client, append([]byte{flagS, 0, 0}, "pc9"...), "", "")
	if !bytes.Equal(d.Reply, append([]byte{flagS, 255, 255}, "pc9.lan.example"...)) {
		t.Errorf("ascii reply %q", d.Reply)
	}
}

func TestRecordTTL(t *testing.T) {
	for _, tc := range []struct {
		ttl, lease time.Duration
		want       uint32
	}{
		{0, 3 * time.Hour, 3600},
		{0, 30 * time.Second, 60},
		{0, 0, 86400},
		{0, 30 * 24 * time.Hour, 86400},
		{5 * time.Minute, time.Hour, 300},
	} {
		if got := RecordTTL(scope.DNSSettings{TTL: tc.ttl}, tc.lease); got != tc.want {
			t.Errorf("RecordTTL(%v, %v) = %d, want %d", tc.ttl, tc.lease, got, tc.want)
		}
	}
}

func FuzzParseClientFQDN(f *testing.F) {
	f.Add(wire(flagS|flagE, "pc1", "corp", "example"))
	f.Add([]byte{flagE, 0, 0, 3, 'p', 'c', '2'})
	f.Add(append([]byte{flagS, 0, 0}, "laptop."...))
	f.Add([]byte{flagE, 0, 0, 63})
	f.Fuzz(func(t *testing.T, b []byte) {
		cf, err := ParseClientFQDN(b)
		if err != nil {
			return
		}
		// Whatever the client sends, the decision yields a valid name or
		// none, and a well-formed reply.
		d := Decide(scope.DNSSettings{Mode: scope.DNSAlways, Domain: "lan.example"}, b, "", "")
		if d.FQDN != "" && !scope.ValidDNSName(d.FQDN) {
			t.Fatalf("invalid name %q from %x", d.FQDN, b)
		}
		if len(d.Reply) < 3 {
			t.Fatalf("short reply %x", d.Reply)
		}
		if cf.E {
			if _, err := ParseClientFQDN(d.Reply); err != nil && d.FQDN != "" {
				t.Fatalf("reply not parseable: %x %v", d.Reply, err)
			}
		}
	})
}

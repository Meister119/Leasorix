package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExportImportRoundTrip(t *testing.T) {
	e := newEnv(t)
	p := e.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	for i := range 3 {
		e.dora(client(i), p)
	}
	ctx := context.Background()
	if _, err := e.m.Release(ctx, client(0), ip("10.0.0.10")); err != nil {
		t.Fatal(err)
	}
	f := NewExport("dhcp-a", e.m.All(), e.clock.Now())
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(f); err != nil {
		t.Fatal(err)
	}

	e2 := newEnv(t)
	e2.scope("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20")
	read, err := ReadExport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := read.Records()
	if err != nil {
		t.Fatal(err)
	}
	changed, conflicts, err := e2.m.Import(ctx, e2.scopes.Snapshot(), recs)
	if err != nil || changed != 3 || conflicts != 0 {
		t.Fatalf("import changed=%d conflicts=%d err=%v", changed, conflicts, err)
	}
	a, b := e.m.All(), e2.m.All()
	for i := range a {
		x, y := a[i], b[i]
		if x.IP != y.IP || x.MAC != y.MAC || x.State != y.State || !x.Expires.Equal(y.Expires) || !x.Updated.Equal(y.Updated) {
			t.Fatalf("record %d differs: %+v vs %+v", i, x, y)
		}
	}
	if changed, _, _ := e2.m.Import(ctx, e2.scopes.Snapshot(), recs); changed != 0 {
		t.Fatal("import is not idempotent")
	}

	for _, bad := range []string{
		`{"format":"other","exported_at":"2026-01-01T00:00:00Z","leases":[]}`,
		`{"format":"leasorix-leases/v1","exported_at":"2026-01-01T00:00:00Z","leases":[],"extra":1}`,
	} {
		if _, err := ReadExport(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	bad := ExportFile{Format: ExportFormat, Leases: []ExportRecord{{IP: "x", MAC: "m", State: StateActive, Expires: time.Now()}}}
	if _, err := bad.Records(); err == nil {
		t.Fatal("invalid address accepted")
	}
}

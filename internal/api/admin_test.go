package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/meister119/leasorix/internal/api/gen"
)

// raw sends a body with an explicit content type.
func (c *client) raw(method, path, ct string, body []byte) resp {
	c.st.t.Helper()
	req, _ := http.NewRequest(method, c.st.srv.URL+"/api/v1"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	if c.csrf != "" {
		req.Header.Set(CSRFHeader, c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.st.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: b, hdr: res.Header}
}

func TestSuperscopesAndScopeFields(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var ss gen.Superscope
	a.must(201, "POST", "/superscopes", gen.SuperscopeInput{Name: "campus"}).json(t, &ss)
	a.must(409, "POST", "/superscopes", gen.SuperscopeInput{Name: "CAMPUS"})
	in := scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")
	in.SuperscopeId = &ss.Id
	in.Bootp = ptr(true)
	var sc gen.Scope
	a.must(201, "POST", "/scopes", in).json(t, &sc)
	if sc.SuperscopeId != ss.Id || !sc.Bootp {
		t.Fatalf("scope %+v", sc)
	}
	// Update without the optional fields keeps them.
	a.must(200, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), scopeInput("lan2", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	if sc.SuperscopeId != ss.Id || !sc.Bootp || sc.Name != "lan2" {
		t.Fatalf("update dropped fields: %+v", sc)
	}
	a.must(200, "GET", fmt.Sprintf("/superscopes/%d", ss.Id), nil).json(t, &ss)
	if len(ss.ScopeIds) != 1 || ss.ScopeIds[0] != sc.Id {
		t.Fatalf("members %+v", ss)
	}
	a.must(200, "PUT", fmt.Sprintf("/superscopes/%d", ss.Id), gen.SuperscopeInput{Name: "campus-2"})
	a.must(204, "DELETE", fmt.Sprintf("/superscopes/%d", ss.Id), nil)
	a.must(200, "GET", fmt.Sprintf("/scopes/%d", sc.Id), nil).json(t, &sc)
	if sc.SuperscopeId != 0 {
		t.Fatal("membership survived superscope deletion")
	}
	bad := scopeInput("x", "10.9.0.0/24", "10.9.0.100", "10.9.0.200")
	bad.SuperscopeId = ptr(int64(999))
	a.must(400, "POST", "/scopes", bad)
}

func TestConfigExportImport(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	r := a.must(200, "GET", "/config/export", nil)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "application/yaml") || !strings.Contains(string(r.body), "format: leasorix-config/v1") {
		t.Fatalf("yaml export: %s %s", r.hdr.Get("Content-Type"), r.body)
	}
	yamlDoc := r.body
	rj := a.must(200, "GET", "/config/export?format=json", nil)
	if !strings.Contains(string(rj.body), `"format": "leasorix-config/v1"`) {
		t.Fatalf("json export: %s", rj.body)
	}

	st2 := newStack(t)
	b := st2.admin()
	var res gen.ConfigImportResult
	b.raw("POST", "/config/import?dry_run=true", "application/yaml", yamlDoc).json(t, &res)
	if res.Scopes != 1 || !res.DryRun || len(st2.scopes.Snapshot().Pools()) != 0 {
		t.Fatalf("dry run %+v", res)
	}
	if r := b.raw("POST", "/config/import", "application/yaml", yamlDoc); r.code != 200 {
		t.Fatalf("import: %d %s", r.code, r.body)
	}
	if len(st2.scopes.Snapshot().Pools()) != 1 {
		t.Fatal("scope not imported")
	}
	if r := b.raw("POST", "/config/import", "application/json", rj.body); r.code != 409 || !strings.Contains(string(r.body), "lan") {
		t.Fatalf("conflicting merge: %d %s", r.code, r.body)
	}
	if r := b.raw("POST", "/config/import?mode=replace", "application/json", rj.body); r.code != 200 {
		t.Fatalf("replace: %d %s", r.code, r.body)
	}
	if r := b.raw("POST", "/config/import", "application/yaml", []byte("format: nope\n")); r.code != 400 {
		t.Fatalf("bad document: %d", r.code)
	}
	if r := b.raw("POST", "/config/import", "image/png", yamlDoc); r.code != 415 {
		t.Fatalf("content type: %d", r.code)
	}
	// netsh dump.
	dump, err := os.ReadFile("../configio/testdata/netsh_dump.txt")
	if err != nil {
		t.Fatal(err)
	}
	st3 := newStack(t)
	c := st3.admin()
	var nr gen.NetshImportResult
	r = c.raw("POST", "/config/import/netsh", "text/plain", dump)
	if r.code != 200 {
		t.Fatalf("netsh import: %d %s", r.code, r.body)
	}
	r.json(t, &nr)
	if nr.Result.Scopes != 2 || len(nr.Warnings) == 0 {
		t.Fatalf("netsh result %+v", nr)
	}
	// Operators may export but not import.
	c.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	op := st3.login("op", "operator-pass")
	op.must(200, "GET", "/config/export", nil)
	if r := op.raw("POST", "/config/import", "application/yaml", yamlDoc); r.code != 403 {
		t.Fatalf("operator import: %d", r.code)
	}
	op.must(403, "GET", "/system/backup", nil)
}

func TestBackupRestoreEndpoints(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200"))
	r := a.must(200, "GET", "/system/backup", nil)
	if r.hdr.Get("Content-Type") != "application/octet-stream" || !bytes.HasPrefix(r.body, []byte("SQLite format 3")) {
		t.Fatalf("backup: %s", r.hdr.Get("Content-Type"))
	}
	backupFile := r.body

	st2 := newStack(t)
	b := st2.admin()
	b.must(201, "POST", "/scopes", scopeInput("other", "10.1.0.0/24", "10.1.0.100", "10.1.0.200"))
	if r := b.raw("POST", "/system/restore", "application/octet-stream", []byte("nonsense")); r.code != 400 {
		t.Fatalf("garbage restore: %d %s", r.code, r.body)
	}
	var res gen.RestoreResult
	r = b.raw("POST", "/system/restore", "application/octet-stream", backupFile)
	if r.code != 200 {
		t.Fatalf("restore: %d %s", r.code, r.body)
	}
	r.json(t, &res)
	if res.Scopes != 1 || res.Users != 1 {
		t.Fatalf("restore result %+v", res)
	}
	if p := st2.scopes.Snapshot().Pools(); len(p) != 1 || p[0].Name != "lan" {
		t.Fatal("scope store not reloaded")
	}
	// The session ended.
	b.must(401, "GET", "/scopes", nil)
	st2.login("admin", "admin-password").must(200, "GET", "/scopes", nil)
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"

	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/scope6"
	"github.com/meister119/leasorix/internal/store"
)

const setupToken = "test-setup-token"

type stack struct {
	t       *testing.T
	srv     *httptest.Server
	scopes  *scope.Store
	leases  *lease.Manager
	scopes6 *scope6.Store
	leases6 *lease6.Manager
	auth    *auth.Service
	server  *Server
}

func newStack(t *testing.T) *stack { return newStackWith(t, nil) }

func newStackWith(t *testing.T, mod func(*Deps)) *stack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	scopes := scope.NewStore(db)
	if err := scopes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	leases := lease.NewManager(lease.DefaultConfig(), log, lease.NewDBStore(db), nil)
	scopes.OnChange(leases.ScopesChanged)
	scopes6 := scope6.NewStore(db)
	if err := scopes6.Load(ctx); err != nil {
		t.Fatal(err)
	}
	leases6 := lease6.NewManager(lease6.DefaultConfig(), log, db)
	scopes6.OnChange(leases6.ScopesChanged)
	done := make(chan struct{})
	go func() { _ = leases.Run(ctx); close(done) }()
	authSvc := auth.NewService(db, auth.Config{SetupToken: setupToken})
	deps := Deps{
		Log: log, Health: health.NewChecker(), Metrics: metrics.New(),
		Auth: authSvc, DB: db, Scopes: scopes, Leases: leases, Audit: audit.NewLog(db), Scopes6: scopes6, Leases6: leases6,
		Info: SystemInfo{StartedAt: time.Now(), Interfaces: []string{"eth0"}}, LoginMaxFailures: 5, LoginWindow: time.Minute,
	}
	if mod != nil {
		mod(&deps)
	}
	s := NewServer("127.0.0.1:0", time.Second, deps)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); cancel(); <-done; _ = db.Close() })
	return &stack{t: t, srv: srv, scopes: scopes, leases: leases, scopes6: scopes6, leases6: leases6, auth: authSvc, server: s}
}

// client is a browser-like client with cookie jar and CSRF token, or an
// API token client.
type client struct {
	st    *stack
	http  *http.Client
	csrf  string
	token string
}

func (st *stack) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{st: st, http: &http.Client{Jar: jar}}
}

type resp struct {
	code int
	body []byte
	hdr  http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (c *client) do(method, path string, body any, hdr ...string) resp {
	c.st.t.Helper()
	var rd io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
		ct = "text/csv"
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.st.srv.URL+"/api/v1"+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", ct)
	}
	if c.csrf != "" {
		req.Header.Set(CSRFHeader, c.csrf)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.st.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: b, hdr: res.Header}
}

func (c *client) must(code int, method, path string, body any) resp {
	c.st.t.Helper()
	r := c.do(method, path, body)
	if r.code != code {
		c.st.t.Fatalf("%s %s: got %d want %d: %s", method, path, r.code, code, r.body)
	}
	return r
}

// admin completes the setup and returns a logged-in admin client.
func (st *stack) admin() *client {
	c := st.client()
	r := c.must(http.StatusCreated, "POST", "/setup", gen.SetupRequest{SetupToken: setupToken, Username: "admin", Password: "admin-password"})
	var sess gen.Session
	r.json(st.t, &sess)
	c.csrf = sess.CsrfToken
	return c
}

func (st *stack) login(user, pw string) *client {
	c := st.client()
	r := c.must(http.StatusOK, "POST", "/auth/login", gen.LoginRequest{Username: user, Password: pw})
	var sess gen.Session
	r.json(st.t, &sess)
	c.csrf = sess.CsrfToken
	return c
}

func scopeInput(name, cidr, start, end string) gen.ScopeInput {
	return gen.ScopeInput{Name: name, Subnet: cidr, Start: start, End: end, LeaseTime: 3600, Enabled: true}
}

func TestSetupFlow(t *testing.T) {
	st := newStack(t)
	c := st.client()
	var ss gen.SetupStatus
	c.must(200, "GET", "/setup", nil).json(t, &ss)
	if !ss.SetupRequired {
		t.Fatal("setup should be required")
	}
	c.must(401, "GET", "/scopes", nil)
	c.must(403, "POST", "/setup", gen.SetupRequest{SetupToken: "wrong", Username: "admin", Password: "admin-password"})
	r := c.must(400, "POST", "/setup", gen.SetupRequest{SetupToken: setupToken, Username: "admin", Password: "short"})
	if !strings.Contains(string(r.body), "password") {
		t.Fatalf("validation detail missing: %s", r.body)
	}
	a := st.admin()
	a.must(200, "GET", "/scopes", nil)
	c.must(200, "GET", "/setup", nil).json(t, &ss)
	if ss.SetupRequired {
		t.Fatal("setup still required")
	}
	c.must(409, "POST", "/setup", gen.SetupRequest{SetupToken: setupToken, Username: "x", Password: "admin-password"})
}

func TestLoginRateLimitAndLogout(t *testing.T) {
	st := newStack(t)
	st.admin()
	c := st.client()
	for range 5 {
		c.must(401, "POST", "/auth/login", gen.LoginRequest{Username: "admin", Password: "wrong-password"})
	}
	r := c.do("POST", "/auth/login", gen.LoginRequest{Username: "admin", Password: "admin-password"})
	if r.code != http.StatusTooManyRequests || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %v", r.code, r.hdr)
	}
	// Failed logins are audited.
	n := 0
	for _, e := range listAudit(t, st.admin2(), "auth.login_failed") {
		if e.ObjectId == "admin" {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("audited failures: %d", n)
	}
}

// admin2 logs in again as admin from a fresh client (rate limit is per IP
// and per user; tests use the loopback IP, so use a direct session).
func (st *stack) admin2() *client {
	c := st.client()
	u, err := st.auth.Authenticate(context.Background(), "admin", "admin-password")
	if err != nil {
		st.t.Fatal(err)
	}
	raw, sess, err := st.auth.CreateSession(context.Background(), u, "127.0.0.1", "test")
	if err != nil {
		st.t.Fatal(err)
	}
	c.http.Jar.SetCookies(mustURL(st.srv.URL), []*http.Cookie{{Name: SessionCookie, Value: raw, Path: "/"}})
	c.csrf = sess.CSRFToken
	return c
}

func listAudit(t *testing.T, c *client, action string) []gen.AuditEntry {
	var al gen.AuditList
	c.must(200, "GET", "/audit?action="+action, nil).json(t, &al)
	return al.Items
}

func TestLogoutAndMe(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var me gen.Session
	a.must(200, "GET", "/auth/me", nil).json(t, &me)
	if me.User.Username != "admin" || me.User.Role != gen.Admin || me.CsrfToken == "" {
		t.Fatalf("me %+v", me)
	}
	a.must(204, "POST", "/auth/logout", nil)
	a.must(401, "GET", "/auth/me", nil)
}

func TestCSRFContentTypeOrigin(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	csrf := a.csrf
	a.csrf = ""
	r := a.do("POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"))
	if r.code != 403 || !strings.Contains(string(r.body), "CSRF") {
		t.Fatalf("missing CSRF accepted: %d %s", r.code, r.body)
	}
	a.csrf = "wrong"
	a.must(403, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"))
	a.csrf = csrf
	r = a.do("POST", "/scopes", `{"name":"x"}`) // text/csv content type
	if r.code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type: %d", r.code)
	}
	r = a.do("POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"), "Origin", "https://evil.example")
	if r.code != 403 {
		t.Fatalf("cross origin: %d", r.code)
	}
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"))
	// Unknown fields are rejected.
	r = a.do("POST", "/scopes", map[string]any{"name": "x", "subnet": "10.1.0.0/24", "start": "10.1.0.1", "end": "10.1.0.2", "lease_time": 60, "enabled": true, "colour": "red"})
	if r.code != 400 {
		t.Fatalf("unknown field: %d", r.code)
	}
}

func TestRolesAndTokens(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(201, "POST", "/users", gen.UserCreate{Username: "ro", Password: "readonly-pass", Role: gen.Readonly})
	a.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})
	a.must(409, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator})

	ro := st.login("ro", "readonly-pass")
	op := st.login("op", "operator-pass")
	ro.must(200, "GET", "/scopes", nil)
	ro.must(403, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"))
	ro.must(403, "GET", "/users", nil)
	op.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.20"))
	op.must(403, "PUT", "/settings", gen.Settings{})
	op.must(403, "POST", "/users", gen.UserCreate{Username: "x", Password: "xxxxxxxxxxxx", Role: gen.Admin})
	op.must(200, "GET", "/settings", nil)
	a.must(200, "PUT", "/settings", gen.Settings{MacFilterDenyEnabled: true})

	// Tokens: readonly token of an operator, then an operator token.
	roleRO := gen.Readonly
	var created gen.ApiTokenCreated
	op.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "ci", Role: &roleRO}).json(t, &created)
	if !strings.HasPrefix(created.Token, "lxt_") {
		t.Fatalf("token %q", created.Token)
	}
	roleAdmin := gen.Admin
	op.must(400, "POST", "/tokens", gen.ApiTokenCreate{Name: "escalate", Role: &roleAdmin})
	tc := &client{st: st, http: &http.Client{}, token: created.Token}
	tc.must(200, "GET", "/scopes", nil)
	tc.must(403, "POST", "/scopes", scopeInput("lan2", "10.1.0.0/24", "10.1.0.10", "10.1.0.20"))
	op.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "deploy"}).json(t, &created)
	tc.token = created.Token
	tc.must(201, "POST", "/scopes", scopeInput("lan2", "10.1.0.0/24", "10.1.0.10", "10.1.0.20")) // no CSRF for tokens
	var me gen.Session
	tc.must(200, "GET", "/auth/me", nil).json(t, &me)
	if me.User.Username != "op" || me.User.Role != gen.Operator {
		t.Fatalf("token me %+v", me)
	}
	var toks []gen.ApiToken
	op.must(200, "GET", "/tokens", nil).json(t, &toks)
	if len(toks) != 2 {
		t.Fatalf("tokens %+v", toks)
	}
	op.must(204, "DELETE", fmt.Sprintf("/tokens/%d", created.Info.Id), nil)
	tc.must(401, "GET", "/scopes", nil)

	// Audit: who did what.
	entries := listAudit(t, a, "scope.create")
	if len(entries) != 2 || entries[0].Actor != "op" || entries[0].Source == nil || !strings.Contains(*entries[0].Source, "token:deploy") {
		t.Fatalf("audit %+v", entries)
	}

	// User management safety.
	var users []gen.User
	a.must(200, "GET", "/users", nil).json(t, &users)
	var adminID int64
	for _, u := range users {
		if u.Username == "admin" {
			adminID = u.Id
		}
	}
	a.must(409, "DELETE", fmt.Sprintf("/users/%d", adminID), nil)
	demote := gen.Readonly
	a.must(409, "PATCH", fmt.Sprintf("/users/%d", adminID), gen.UserUpdate{Role: &demote})
	// Disabling a user ends its session.
	var roUser gen.User
	for _, u := range users {
		if u.Username == "ro" {
			roUser = u
		}
	}
	yes := true
	a.must(200, "PATCH", fmt.Sprintf("/users/%d", roUser.Id), gen.UserUpdate{Disabled: &yes})
	ro.must(401, "GET", "/scopes", nil)
}

func TestPasswordChange(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	a.must(403, "POST", "/auth/password", gen.PasswordChange{CurrentPassword: "nope-nope-nope", NewPassword: "new-admin-password"})
	a.must(400, "POST", "/auth/password", gen.PasswordChange{CurrentPassword: "admin-password", NewPassword: "short"})
	a.must(204, "POST", "/auth/password", gen.PasswordChange{CurrentPassword: "admin-password", NewPassword: "new-admin-password"})
	st.login("admin", "new-admin-password")
}

func TestScopeCRUDAndValidation(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	r := a.must(400, "POST", "/scopes", scopeInput("bad", "10.0.0.0/24", "10.0.1.10", "10.0.0.20"))
	var e gen.Error
	r.json(t, &e)
	if e.Fields == nil || (*e.Fields)[0].Field != "start" {
		t.Fatalf("field errors %+v", e)
	}
	a.must(400, "POST", "/scopes", scopeInput("bad", "not-a-net", "10.0.0.10", "10.0.0.20"))
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.200")).json(t, &sc)
	a.must(409, "POST", "/scopes", scopeInput("other", "10.0.0.128/25", "10.0.0.130", "10.0.0.140"))
	a.must(404, "GET", "/scopes/999", nil)
	a.must(400, "GET", "/scopes/abc", nil)

	in := scopeInput("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.150")
	desc := "office"
	in.Description = &desc
	var upd gen.Scope
	a.must(200, "PUT", fmt.Sprintf("/scopes/%d", sc.Id), in).json(t, &upd)
	if upd.Description != "office" || upd.End != "10.0.0.150" {
		t.Fatalf("update %+v", upd)
	}
	var ex gen.Exclusion
	a.must(201, "POST", fmt.Sprintf("/scopes/%d/exclusions", sc.Id), gen.AddrRange{Start: "10.0.0.10", End: "10.0.0.19"}).json(t, &ex)
	a.must(400, "POST", fmt.Sprintf("/scopes/%d/exclusions", sc.Id), gen.AddrRange{Start: "10.0.1.10", End: "10.0.1.19"})
	var exs []gen.Exclusion
	a.must(200, "GET", fmt.Sprintf("/scopes/%d/exclusions", sc.Id), nil).json(t, &exs)
	if len(exs) != 1 {
		t.Fatalf("exclusions %+v", exs)
	}
	var stats gen.ScopeStats
	a.must(200, "GET", fmt.Sprintf("/scopes/%d/stats", sc.Id), nil).json(t, &stats)
	if stats.Usable != 131 {
		t.Fatalf("stats %+v", stats)
	}
	a.must(204, "DELETE", fmt.Sprintf("/scopes/%d/exclusions/%d", sc.Id, ex.Id), nil)

	// Options at several levels.
	var ov gen.OptionValue
	a.must(200, "PUT", fmt.Sprintf("/options/scope/%d/6", sc.Id), gen.OptionValueInput{Value: "1.1.1.1, 9.9.9.9"}).json(t, &ov)
	if ov.Value != "1.1.1.1,9.9.9.9" || ov.Name != "domain-name-servers" || ov.Type != gen.IpList {
		t.Fatalf("option %+v", ov)
	}
	a.must(400, "PUT", "/options/server/0/1", gen.OptionValueInput{Value: "255.255.255.0"})
	a.must(400, "PUT", "/options/server/0/6", gen.OptionValueInput{Value: "dns.example"})
	a.must(400, "PUT", "/options/scope/0/6", gen.OptionValueInput{Value: "1.1.1.1"})
	a.must(404, "PUT", "/options/scope/999/6", gen.OptionValueInput{Value: "1.1.1.1"})
	a.must(200, "PUT", "/options/server/0/15", gen.OptionValueInput{Value: "example.com"})
	var opts []gen.OptionValue
	a.must(200, "GET", "/options/server/0", nil).json(t, &opts)
	if len(opts) != 1 || opts[0].Code != 15 {
		t.Fatalf("server options %+v", opts)
	}
	a.must(204, "DELETE", "/options/server/0/15", nil)
	a.must(404, "DELETE", "/options/server/0/15", nil)
	var defs []gen.OptionDefinition
	a.must(200, "GET", "/option-definitions", nil).json(t, &defs)
	if len(defs) < 40 {
		t.Fatalf("definitions %d", len(defs))
	}
	a.must(201, "POST", "/option-definitions", gen.OptionDefinitionInput{Code: 224, Name: "site", Type: gen.String})
	a.must(204, "DELETE", "/option-definitions/224", nil)

	// Classes, policies, filters.
	a.must(201, "POST", "/classes", gen.ClassInput{Name: "PXE", Type: gen.ClassInputTypeVendor, Data: "PXEClient*"})
	var pol gen.Policy
	ranges := []gen.AddrRange{{Start: "10.0.0.140", End: "10.0.0.150"}}
	a.must(201, "POST", "/policies", gen.PolicyInput{ScopeId: &sc.Id, Name: "pxe", Enabled: true, Operator: gen.PolicyInputOperatorAnd,
		Conditions: []gen.Condition{{Field: gen.ConditionFieldClass, Operator: gen.Eq, Value: "PXE"}}, Ranges: &ranges}).json(t, &pol)
	if pol.Order != 1 || len(pol.Ranges) != 1 {
		t.Fatalf("policy %+v", pol)
	}
	a.must(400, "POST", "/policies", gen.PolicyInput{Name: "empty", Enabled: true, Operator: gen.PolicyInputOperatorAnd, Conditions: []gen.Condition{}})
	a.must(200, "PUT", fmt.Sprintf("/options/policy/%d/67", pol.Id), gen.OptionValueInput{Value: "pxelinux.0"})
	var pols []gen.Policy
	a.must(200, "GET", fmt.Sprintf("/policies?scope_id=%d", sc.Id), nil).json(t, &pols)
	if len(pols) != 1 {
		t.Fatalf("policies %+v", pols)
	}
	a.must(200, "GET", "/policies?scope_id=0", nil).json(t, &pols)
	if len(pols) != 0 {
		t.Fatalf("server policies %+v", pols)
	}
	var classes []gen.Class
	a.must(200, "GET", "/classes", nil).json(t, &classes)
	a.must(409, "DELETE", fmt.Sprintf("/classes/%d", classes[0].Id), nil)
	a.must(204, "DELETE", fmt.Sprintf("/policies/%d", pol.Id), nil)
	a.must(204, "DELETE", fmt.Sprintf("/classes/%d", classes[0].Id), nil)
	var f gen.MacFilter
	a.must(201, "POST", "/filters", gen.MacFilterInput{List: gen.MacFilterInputListDeny, Pattern: "AA-BB-*"}).json(t, &f)
	if f.Pattern != "aa:bb:*" {
		t.Fatalf("filter %+v", f)
	}
	a.must(204, "DELETE", fmt.Sprintf("/filters/%d", f.Id), nil)

	a.must(204, "DELETE", fmt.Sprintf("/scopes/%d", sc.Id), nil)
	a.must(404, "DELETE", fmt.Sprintf("/scopes/%d", sc.Id), nil)
}

func TestReservationsAndImport(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	mac := "02:00:00:00:00:01"
	var res gen.Reservation
	a.must(201, "POST", "/reservations", gen.ReservationInput{ScopeId: sc.Id, Ip: "10.0.0.5", Mac: &mac}).json(t, &res)
	a.must(409, "POST", "/reservations", gen.ReservationInput{ScopeId: sc.Id, Ip: "10.0.0.5", Mac: strPtr("02:00:00:00:00:02")})
	a.must(400, "POST", "/reservations", gen.ReservationInput{ScopeId: sc.Id, Ip: "10.0.0.6"})
	a.must(400, "POST", "/reservations", gen.ReservationInput{ScopeId: sc.Id, Ip: "nope", Mac: &mac})
	res2 := gen.ReservationInput{ScopeId: sc.Id, Ip: "10.0.0.7", Mac: &mac, Hostname: strPtr("printer")}
	a.must(200, "PUT", fmt.Sprintf("/reservations/%d", res.Id), res2)

	bad := "scope;ip;mac;hostname\nlan;10.0.0.20;02:00:00:00:00:10;a\nlan;10.0.0.21;garbage;b\nnope;10.0.0.22;02:00:00:00:00:12;c\n"
	var ir gen.ImportResult
	a.must(200, "POST", "/reservations/import", bad).json(t, &ir)
	if ir.Created != 0 || len(ir.Errors) != 1 || ir.Errors[0].Line != 4 {
		t.Fatalf("import parse errors %+v", ir)
	}
	bad = "scope,ip,mac\nlan,10.0.0.20,02:00:00:00:00:10\nlan,10.0.0.21,garbage\n"
	a.must(200, "POST", "/reservations/import", bad).json(t, &ir)
	if ir.Created != 0 || len(ir.Errors) != 1 || ir.Errors[0].Line != 3 {
		t.Fatalf("import validation errors %+v", ir)
	}
	good := "\xef\xbb\xbfScope,IP,MAC,Hostname,Description\nlan,10.0.0.20,02:00:00:00:00:10,a,\"desc, with comma\"\n" + fmt.Sprint(sc.Id) + ",10.0.0.21,02:00:00:00:00:11,b,\n"
	a.must(200, "POST", "/reservations/import", good).json(t, &ir)
	if ir.Created != 2 || len(ir.Errors) != 0 {
		t.Fatalf("import %+v", ir)
	}
	var all []gen.Reservation
	a.must(200, "GET", fmt.Sprintf("/reservations?scope_id=%d", sc.Id), nil).json(t, &all)
	if len(all) != 3 {
		t.Fatalf("reservations %+v", all)
	}
	a.must(204, "DELETE", fmt.Sprintf("/reservations/%d", res.Id), nil)
}

func TestLeasesEndpoints(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var sc gen.Scope
	a.must(201, "POST", "/scopes", scopeInput("lan", "10.0.0.0/24", "10.0.0.100", "10.0.0.200")).json(t, &sc)
	ctx := context.Background()
	pool := st.scopes.Snapshot().ByID(sc.Id)
	for i := range 3 {
		c := lease.Client{MAC: fmt.Sprintf("02:00:00:00:00:%02x", i), Hostname: fmt.Sprintf("host-%d", i)}
		o := st.leases.Offer(ctx, c, []lease.Candidate{lease.PoolCandidate(pool)}, 0)
		if _, err := st.leases.Request(ctx, c, lease.PoolCandidate(pool), o.IP); err != nil {
			t.Fatal(err)
		}
	}
	var ll gen.LeaseList
	a.must(200, "GET", "/leases", nil).json(t, &ll)
	if ll.Total != 3 || ll.Items[0].Ip != "10.0.0.100" || ll.Items[0].ScopeName != "lan" || ll.Items[0].State != gen.Active {
		t.Fatalf("leases %+v", ll)
	}
	a.must(200, "GET", "/leases?q=HOST-1", nil).json(t, &ll)
	if ll.Total != 1 || ll.Items[0].Hostname != "host-1" {
		t.Fatalf("search %+v", ll)
	}
	a.must(200, "GET", "/leases?limit=2&offset=2", nil).json(t, &ll)
	if ll.Total != 3 || len(ll.Items) != 1 {
		t.Fatalf("paging %+v", ll)
	}
	a.must(200, "GET", "/leases?state=released", nil).json(t, &ll)
	if ll.Total != 0 || ll.Items == nil {
		t.Fatalf("state filter %+v", ll)
	}
	var res gen.Reservation
	a.must(201, "POST", "/leases/10.0.0.101/reservation", map[string]string{"description": "from lease"}).json(t, &res)
	if res.Ip != "10.0.0.101" || res.Mac != "02:00:00:00:00:01" || res.Hostname != "host-1" || res.Description != "from lease" {
		t.Fatalf("converted %+v", res)
	}
	a.must(409, "POST", "/leases/10.0.0.101/reservation", nil)
	var l gen.Lease
	a.must(200, "GET", "/leases/10.0.0.101", nil).json(t, &l)
	if !l.Reserved {
		t.Fatal("lease not marked reserved")
	}
	a.must(204, "DELETE", "/leases/10.0.0.102", nil)
	a.must(404, "GET", "/leases/10.0.0.102", nil)
	a.must(404, "DELETE", "/leases/10.0.0.102", nil)
	a.must(400, "GET", "/leases/not-an-ip", nil)
	if e := listAudit(t, a, "lease.delete"); len(e) != 1 || e[0].Actor != "admin" {
		t.Fatalf("lease.delete audit %+v", e)
	}

	var stats gen.Stats
	a.must(200, "GET", "/stats", nil).json(t, &stats)
	if stats.ActiveLeases != 2 || len(stats.Scopes) != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestSystemEndpointsAndHeaders(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var info gen.SystemInfo
	r := a.must(200, "GET", "/system", nil)
	r.json(t, &info)
	if info.Version == "" || len(info.Dhcp4Interfaces) != 1 {
		t.Fatalf("info %+v", info)
	}
	if r.hdr.Get("Content-Security-Policy") == "" || r.hdr.Get("X-Frame-Options") != "DENY" || r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", r.hdr)
	}
	var ha gen.HAStatus
	a.must(200, "GET", "/ha/status", nil).json(t, &ha)
	if ha.Enabled {
		t.Fatal("ha enabled")
	}
	r = a.do("GET", "/nope", nil)
	if r.code != 404 || !strings.Contains(string(r.body), "error") {
		t.Fatalf("unknown endpoint: %d %s", r.code, r.body)
	}
	// Session cookie flags.
	c := st.client()
	res := c.do("POST", "/auth/login", gen.LoginRequest{Username: "admin", Password: "admin-password"})
	ck := res.hdr.Get("Set-Cookie")
	if !strings.Contains(ck, "HttpOnly") || !strings.Contains(ck, "SameSite=Strict") {
		t.Fatalf("cookie %s", ck)
	}
	// The UI placeholder is served for other paths.
	resp, err := http.Get(st.srv.URL + "/scopes/1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ui fallback %d", resp.StatusCode)
	}
}

func TestRequiredRoleTable(t *testing.T) {
	cases := []struct {
		method, path string
		role         auth.Role
		public       bool
	}{
		{"POST", "/api/v1/auth/login", "", true},
		{"GET", "/api/v1/setup", "", true},
		{"GET", "/api/v1/scopes", auth.RoleReadonly, false},
		{"POST", "/api/v1/scopes", auth.RoleOperator, false},
		{"DELETE", "/api/v1/leases/10.0.0.1", auth.RoleOperator, false},
		{"GET", "/api/v1/users", auth.RoleAdmin, false},
		{"PATCH", "/api/v1/users/2", auth.RoleAdmin, false},
		{"GET", "/api/v1/settings", auth.RoleReadonly, false},
		{"PUT", "/api/v1/settings", auth.RoleAdmin, false},
		{"POST", "/api/v1/tokens", auth.RoleReadonly, false},
		{"POST", "/api/v1/auth/password", auth.RoleReadonly, false},
	}
	for _, c := range cases {
		role, public := requiredRole(c.method, c.path)
		if role != c.role || public != c.public {
			t.Errorf("%s %s = %s/%v", c.method, c.path, role, public)
		}
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

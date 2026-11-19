package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/meister119/leasorix/internal/api/gen"
)

// An API token must not manage tokens or the password of its user: a leaked
// read-only token could otherwise revoke the admin's tokens or lock the
// account (audit finding).
func TestTokenCannotManageCredentials(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var adminTok, roTok gen.ApiTokenCreated
	a.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "automation"}).json(t, &adminTok)
	ro := gen.Readonly
	a.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "ro", Role: &ro}).json(t, &roTok)

	tc := &client{st: st, http: &http.Client{}, token: roTok.Token}
	tc.must(200, "GET", "/scopes", nil)
	tc.must(200, "GET", "/auth/me", nil)
	tc.must(403, "GET", "/tokens", nil)
	tc.must(403, "POST", "/tokens", gen.ApiTokenCreate{Name: "more", Role: &ro})
	tc.must(403, "DELETE", fmt.Sprintf("/tokens/%d", adminTok.Info.Id), nil)
	tc.must(403, "POST", "/auth/password", gen.PasswordChange{CurrentPassword: "admin-password", NewPassword: "taken-over-pw"})

	// Admin role tokens are restricted in the same way.
	tc.token = adminTok.Token
	tc.must(403, "GET", "/tokens", nil)
	tc.must(200, "GET", "/users", nil)

	// The web session still manages tokens.
	var toks []gen.ApiToken
	a.must(200, "GET", "/tokens", nil).json(t, &toks)
	if len(toks) != 2 {
		t.Fatalf("tokens %+v", toks)
	}
}

// Changing the password (own or by an admin) revokes the user's API tokens.
func TestPasswordChangeRevokesTokens(t *testing.T) {
	st := newStack(t)
	a := st.admin()
	var tok gen.ApiTokenCreated
	a.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "ci"}).json(t, &tok)
	tc := &client{st: st, http: &http.Client{}, token: tok.Token}
	tc.must(200, "GET", "/scopes", nil)
	a.must(204, "POST", "/auth/password", gen.PasswordChange{CurrentPassword: "admin-password", NewPassword: "new-admin-password"})
	tc.must(401, "GET", "/scopes", nil)
	a.must(200, "GET", "/scopes", nil) // the current session survives

	// Password reset of another user by an admin.
	var u gen.User
	a.must(201, "POST", "/users", gen.UserCreate{Username: "op", Password: "operator-pass", Role: gen.Operator}).json(t, &u)
	op := st.login("op", "operator-pass")
	op.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "deploy"}).json(t, &tok)
	tc.token = tok.Token
	tc.must(200, "GET", "/scopes", nil)
	pw := "reset-by-admin"
	a.must(200, "PATCH", fmt.Sprintf("/users/%d", u.Id), gen.UserUpdate{Password: &pw})
	tc.must(401, "GET", "/scopes", nil)
}

// The per-user lockout is keyed by user and source address; failures of
// one source block that source only for this user.
func TestLoginLockoutPerSource(t *testing.T) {
	st := newStack(t) // LoginMaxFailures: 5
	st.admin()
	c := st.client()
	for i := 0; i < 5; i++ {
		c.must(401, "POST", "/auth/login", gen.LoginRequest{Username: "admin", Password: "wrong-password"})
	}
	c.must(429, "POST", "/auth/login", gen.LoginRequest{Username: "admin", Password: "admin-password"})
	// Another source address is not affected by the per-user counter.
	if blocked, _ := st.server.userLimiter.Blocked("user:admin|ip:10.9.9.9"); blocked {
		t.Fatal("other source must not be locked out")
	}
	if blocked, _ := st.server.userLimiter.Blocked("user:admin|ip:127.0.0.1"); !blocked {
		t.Fatal("failing source must be locked out")
	}
}

func TestMetricsAuth(t *testing.T) {
	get := func(st *stack, token string) int {
		req, _ := http.NewRequest(http.MethodGet, st.srv.URL+"/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	open := newStack(t)
	if code := get(open, ""); code != 200 {
		t.Fatalf("metrics without metrics_auth: %d", code)
	}

	st := newStackWith(t, func(d *Deps) { d.MetricsAuth = true })
	if code := get(st, ""); code != 401 {
		t.Fatalf("anonymous metrics: %d", code)
	}
	if code := get(st, "lxt_invalid"); code != 401 {
		t.Fatalf("invalid token metrics: %d", code)
	}
	a := st.admin()
	ro := gen.Readonly
	var tok gen.ApiTokenCreated
	a.must(201, "POST", "/tokens", gen.ApiTokenCreate{Name: "prometheus", Role: &ro}).json(t, &tok)
	if code := get(st, tok.Token); code != 200 {
		t.Fatalf("token metrics: %d", code)
	}
}

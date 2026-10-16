package api

import (
	"context"
	"crypto/subtle"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
)

// SessionCookie is the name of the web session cookie.
const SessionCookie = "leasorix_session"

// CSRFHeader carries the CSRF token on state changing requests.
const CSRFHeader = "X-CSRF-Token"

const apiPrefix = "/api/v1"

type principalKey struct{}

func principalFrom(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalKey{}).(*auth.Principal)
	return p
}

// clientIP returns the peer address. X-Forwarded-For is deliberately
// ignored (see DECISIONS D-008).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func securityHeaders(tlsOn bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			if tlsOn {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(1 << 20)
		switch r.URL.Path {
		case apiPrefix + "/reservations/import", apiPrefix + "/scopes/import":
			limit = 8 << 20
		case apiPrefix + "/leases/import", apiPrefix + "/config/import", apiPrefix + "/config/import/netsh":
			limit = 64 << 20
		case apiPrefix + "/system/restore":
			limit = 1 << 30
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the caller from an API token or the session cookie.
// It never rejects; authorize does.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var p *auth.Principal
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if tp, err := s.deps.Auth.LookupToken(ctx, strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))); err == nil {
				p = tp
			} else {
				writeError(w, http.StatusUnauthorized, "invalid API token", nil)
				return
			}
		} else if c, err := r.Cookie(SessionCookie); err == nil {
			if sess, err := s.deps.Auth.LookupSession(ctx, c.Value); err == nil {
				p = &auth.Principal{User: sess.User, Role: sess.User.Role, Session: sess}
			}
		}
		source := clientIP(r)
		if p != nil {
			if p.Token != nil {
				source += " token:" + p.Token.Name
			}
			ctx = audit.WithActor(ctx, p.User.Username, source)
			ctx = context.WithValue(ctx, principalKey{}, p)
		} else {
			ctx = audit.WithActor(ctx, "anonymous", source)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requiredRole maps a request to the role it needs. public = no
// authentication required.
func requiredRole(method, path string) (role auth.Role, public bool) {
	p := strings.TrimPrefix(path, apiPrefix)
	read := method == http.MethodGet || method == http.MethodHead
	switch {
	case p == "/setup" || p == "/auth/login":
		return "", true
	case strings.HasPrefix(p, "/users"):
		return auth.RoleAdmin, false
	case p == "/settings" && !read:
		return auth.RoleAdmin, false
	case strings.HasPrefix(p, "/ha/") && !read, p == "/leases/import", strings.HasPrefix(p, "/config/import"),
		p == "/system/backup", p == "/system/restore":
		return auth.RoleAdmin, false
	case p == "/auth/logout" || p == "/auth/me" || p == "/auth/password" || strings.HasPrefix(p, "/tokens"):
		return auth.RoleReadonly, false
	case read:
		return auth.RoleReadonly, false
	}
	return auth.RoleOperator, false
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if mutating && r.ContentLength != 0 && !s.contentTypeOK(r) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported content type", nil)
			return
		}
		if mutating && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected", nil)
			return
		}
		need, public := requiredRole(r.Method, r.URL.Path)
		if public {
			next.ServeHTTP(w, r)
			return
		}
		p := principalFrom(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "authentication required", nil)
			return
		}
		if mutating && p.Session != nil {
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(p.Session.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "missing or invalid CSRF token", nil)
				return
			}
		}
		if !p.Role.AtLeast(need) {
			writeError(w, http.StatusForbidden, "your role ("+string(p.Role)+") does not allow this action", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isConfigWrite reports whether a request changes replicated
// configuration. Lease operations, login/logout and HA actions are local.
func isConfigWrite(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return false
	}
	p := strings.TrimPrefix(path, apiPrefix)
	switch {
	case p == "/setup", p == "/auth/login", p == "/auth/logout", strings.HasPrefix(p, "/ha/"):
		return false
	case strings.HasPrefix(p, "/leases"):
		// Converting a lease creates a reservation (configuration).
		return strings.HasSuffix(p, "/reservation")
	}
	return true
}

// configWritable rejects configuration changes on the HA partner that is
// not the configuration leader.
func (s *Server) configWritable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.HA != nil && s.deps.HA.ConfigReadOnly() && isConfigWrite(r.Method, r.URL.Path) {
			st := s.deps.HA.Status()
			writeError(w, http.StatusConflict, "configuration is read-only on this server: make changes on the HA config leader "+st.PeerID+" ("+st.PeerAddress+")", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) contentTypeOK(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	switch r.URL.Path {
	case apiPrefix + "/reservations/import", apiPrefix + "/config/import/netsh":
		return mt == "text/csv" || mt == "text/plain"
	case apiPrefix + "/config/import":
		switch mt {
		case "application/json", "application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml", "text/plain":
			return true
		}
		return false
	case apiPrefix + "/system/restore":
		return mt == "application/octet-stream" || mt == "application/x-sqlite3" || mt == "application/vnd.sqlite3"
	}
	return mt == "application/json"
}

// sameOrigin rejects browser requests whose Origin does not match the
// host (defence in depth on top of SameSite cookies and the CSRF token).
// Requests without Origin (non-browser clients) are allowed.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

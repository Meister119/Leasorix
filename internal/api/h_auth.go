package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/auth"
)

// handlers implements gen.ServerInterface.
type handlers struct {
	s *Server
}

var _ gen.ServerInterface = (*handlers)(nil)

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	fail(h.s.deps.Log, w, r, err)
}

func (h *handlers) setSessionCookie(w http.ResponseWriter, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the listener: set whenever TLS is enabled
		Name: SessionCookie, Value: raw, Path: "/", Expires: expires,
		HttpOnly: true, Secure: h.s.deps.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func toSession(sess auth.Session) gen.Session {
	return gen.Session{User: toUser(sess.User), CsrfToken: sess.CSRFToken, ExpiresAt: sess.ExpiresAt}
}

func (h *handlers) GetSetupStatus(w http.ResponseWriter, r *http.Request) {
	req, err := h.s.deps.Auth.SetupRequired(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.SetupStatus{SetupRequired: req})
}

func (h *handlers) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	var in gen.SetupRequest
	if !decode(w, r, &in) {
		return
	}
	ip := clientIP(r)
	if blocked, wait := h.s.ipLimiter.Blocked("ip:" + ip); blocked {
		tooMany(w, wait)
		return
	}
	u, err := h.s.deps.Auth.CompleteSetup(r.Context(), in.SetupToken, in.Username, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			h.s.ipLimiter.Fail("ip:" + ip)
		}
		h.fail(w, r, err)
		return
	}
	h.s.deps.Log.Info("initial setup completed", "admin", u.Username)
	raw, sess, err := h.s.deps.Auth.CreateSession(r.Context(), u, ip, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setSessionCookie(w, raw, sess.ExpiresAt)
	writeJSON(w, http.StatusCreated, toSession(sess))
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later", nil)
}

// busy answers 503 when all password check slots are in use.
func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeError(w, http.StatusServiceUnavailable, "server busy, try again later", nil)
}

func (h *handlers) Login(w http.ResponseWriter, r *http.Request) {
	var in gen.LoginRequest
	if !decode(w, r, &in) {
		return
	}
	ip := clientIP(r)
	// The per-user lockout is keyed by user and source address: a third
	// party sending wrong passwords from elsewhere must not be able to lock
	// a legitimate user (e.g. the only admin) out. Guessing from many
	// addresses is still bounded by the per-address limit of each of them.
	userKey := "user:" + strings.ToLower(strings.TrimSpace(in.Username)) + "|ip:" + ip
	if blocked, wait := h.s.ipLimiter.Blocked("ip:" + ip); blocked {
		tooMany(w, wait)
		return
	}
	if blocked, wait := h.s.userLimiter.Blocked(userKey); blocked {
		tooMany(w, wait)
		return
	}
	u, err := h.s.deps.Auth.Authenticate(r.Context(), in.Username, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.s.ipLimiter.Fail("ip:" + ip)
			h.s.userLimiter.Fail(userKey)
			h.s.deps.Auth.RecordLoginFailure(r.Context(), in.Username, "invalid credentials")
			writeError(w, http.StatusUnauthorized, "invalid username or password", nil)
			return
		}
		if errors.Is(err, auth.ErrBusy) {
			busy(w)
			return
		}
		h.fail(w, r, err)
		return
	}
	h.s.userLimiter.Reset(userKey)
	raw, sess, err := h.s.deps.Auth.CreateSession(r.Context(), u, ip, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setSessionCookie(w, raw, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, toSession(sess))
}

func (h *handlers) Logout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.Session != nil {
		if err := h.s.deps.Auth.DeleteSession(r.Context(), p.Session); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.setSessionCookie(w, "", time.Unix(0, 0))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) GetMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.Session != nil {
		writeJSON(w, http.StatusOK, toSession(*p.Session))
		return
	}
	expires := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if p.Token.ExpiresAt != nil {
		expires = *p.Token.ExpiresAt
	}
	u := toUser(p.User)
	u.Role = gen.Role(p.Role)
	writeJSON(w, http.StatusOK, gen.Session{User: u, ExpiresAt: expires})
}

func (h *handlers) ChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var in gen.PasswordChange
	if !decode(w, r, &in) {
		return
	}
	p := principalFrom(r.Context())
	if err := h.s.deps.Auth.ChangePassword(r.Context(), p.Session, p.User.ID, in.CurrentPassword, in.NewPassword); err != nil {
		if errors.Is(err, auth.ErrBusy) {
			busy(w)
			return
		}
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	ts, err := h.s.deps.Auth.ListTokens(r.Context(), p.User.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.ApiToken, len(ts))
	for i, t := range ts {
		out[i] = toToken(t)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	var in gen.ApiTokenCreate
	if !decode(w, r, &in) {
		return
	}
	p := principalFrom(r.Context())
	var role auth.Role
	if in.Role != nil {
		role = auth.Role(*in.Role)
	}
	// A token can never carry more rights than the caller currently has.
	user := p.User
	user.Role = p.Role
	var ttl time.Duration
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays < 1 || *in.ExpiresInDays > 3650 {
			badRequest(w, "expires_in_days", "must be between 1 and 3650")
			return
		}
		ttl = time.Duration(*in.ExpiresInDays) * 24 * time.Hour
	}
	raw, t, err := h.s.deps.Auth.CreateToken(r.Context(), user, in.Name, role, ttl)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, gen.ApiTokenCreated{Token: raw, Info: toToken(t)})
}

func (h *handlers) DeleteToken(w http.ResponseWriter, r *http.Request, id gen.Id) {
	p := principalFrom(r.Context())
	if err := h.s.deps.Auth.DeleteToken(r.Context(), p.User.ID, id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/store"
)

// Role of a user or token.
type Role string

// Roles in ascending order of privilege.
const (
	RoleReadonly Role = "readonly"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

func (r Role) level() int {
	switch r {
	case RoleReadonly:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r.level() > 0 }

// AtLeast reports whether r has at least the privileges of min.
func (r Role) AtLeast(min Role) bool { return r.level() >= min.level() && r.Valid() }

// User is a local account.
type User struct {
	ID        int64
	Username  string
	Role      Role
	Disabled  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is an authenticated web session.
type Session struct {
	User      User
	CSRFToken string
	ExpiresAt time.Time
	idHash    string
}

// Token is an API token (the secret itself is never stored).
type Token struct {
	ID         int64
	UserID     int64
	Name       string
	Prefix     string
	Role       Role
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

// Principal is the authenticated caller of a request.
type Principal struct {
	User User
	// Role is the effective role (a token may have a lower role than its
	// user).
	Role Role
	// Session is set for cookie authenticated requests (CSRF applies).
	Session *Session
	// Token is set for API token requests.
	Token *Token
}

// Config tunes sessions.
type Config struct {
	SessionTTL  time.Duration // absolute lifetime
	IdleTimeout time.Duration // inactivity timeout
	// SetupToken pre-defines the one-time setup token (otherwise random).
	SetupToken string
}

// ErrInvalidCredentials is returned for a failed login.
var ErrInvalidCredentials = errors.New("invalid username or password")

// Service manages users, sessions and tokens.
type Service struct {
	db  *store.DB
	cfg Config
	now func() time.Time

	setupMu    sync.Mutex
	setupToken string
}

// NewService creates the service.
func NewService(db *store.DB, cfg Config) *Service {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = time.Hour
	}
	return &Service{db: db, cfg: cfg, now: time.Now}
}

// SetClock overrides the time source (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func hashSecret(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ---- setup -------------------------------------------------------------------

// SetupRequired reports whether no user exists yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// SetupToken returns the one-time token needed to create the first admin.
// It is generated on first use (or taken from the config).
func (s *Service) SetupToken() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupToken == "" {
		s.setupToken = s.cfg.SetupToken
		if s.setupToken == "" {
			s.setupToken = randomToken(18)
		}
	}
	return s.setupToken
}

// CompleteSetup creates the first admin if no user exists and token matches.
func (s *Service) CompleteSetup(ctx context.Context, token, username, password string) (User, error) {
	if required, err := s.SetupRequired(ctx); err != nil {
		return User{}, err
	} else if !required {
		return User{}, apperr.Conflictf("setup already completed")
	}
	want := s.SetupToken()
	if subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return User{}, fmt.Errorf("invalid setup token: %w", ErrForbidden)
	}
	var u User
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("setup already completed")
		}
		var err error
		u, err = s.createUserTx(ctx, tx, username, password, RoleAdmin)
		if err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(audit.WithActor(ctx, u.Username, sourceOf(ctx)), "auth.setup", "user", fmt.Sprint(u.ID),
			map[string]any{"username": u.Username, "role": string(u.Role)}))
	})
	if err != nil {
		return User{}, err
	}
	s.setupMu.Lock()
	s.setupToken = "" // one-time
	s.cfg.SetupToken = ""
	s.setupMu.Unlock()
	return u, nil
}

// ErrForbidden marks operations the caller may not perform.
var ErrForbidden = errors.New("forbidden")

func sourceOf(ctx context.Context) string {
	_, src := audit.ActorFrom(ctx)
	return src
}

// ---- users -------------------------------------------------------------------

const userCols = `id, username, role, disabled, created_at, updated_at`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var u User
	var role string
	var created, updated int64
	if err := r.Scan(&u.ID, &u.Username, &role, &u.Disabled, &created, &updated); err != nil {
		return u, err
	}
	u.Role = Role(role)
	u.CreatedAt, u.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return u, nil
}

func validateUsername(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fieldErr("username", "must be 1-64 characters")
	}
	for _, r := range name {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && !strings.ContainsRune("._@-", r) {
			return fieldErr("username", "may only contain letters, digits and . _ @ -")
		}
	}
	return nil
}

func validatePassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return fieldErr("password", fmt.Sprintf("must be at least %d characters", MinPasswordLen))
	}
	if len(pw) > 1024 {
		return fieldErr("password", "too long")
	}
	return nil
}

func fieldErr(field, msg string) error {
	var v apperr.Validator
	v.Add(field, "%s", msg)
	return v.Err()
}

func (s *Service) createUserTx(ctx context.Context, tx *sql.Tx, username, password string, role Role) (User, error) {
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if !role.Valid() {
		return User{}, fieldErr("role", "must be admin, operator or readonly")
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n); err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, apperr.Conflictf("user %q already exists", username)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	res, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, disabled, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
		username, hash, string(role), now.Unix(), now.Unix())
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: role, CreatedAt: now, UpdatedAt: now}, nil
}

// CreateUser adds a user.
func (s *Service) CreateUser(ctx context.Context, username, password string, role Role) (User, error) {
	var u User
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if u, err = s.createUserTx(ctx, tx, username, password, role); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "user.create", "user", fmt.Sprint(u.ID),
			map[string]any{"username": u.Username, "role": string(u.Role)}))
	})
	return u, err
}

// ListUsers returns all users ordered by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns one user.
func (s *Service) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, apperr.NotFoundf("user %d", id)
	}
	return u, err
}

// UserUpdate holds optional changes.
type UserUpdate struct {
	Role     *Role
	Disabled *bool
	Password *string
}

func (s *Service) otherActiveAdmins(ctx context.Context, tx *sql.Tx, id int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id <> ?`, id).Scan(&n)
	return n, err
}

// UpdateUser changes role, disabled flag and/or password. The last enabled
// admin cannot be demoted or disabled. Disabling a user or changing the
// password ends the user's sessions.
func (s *Service) UpdateUser(ctx context.Context, id int64, up UserUpdate) (User, error) {
	var u User
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		old, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("user %d", id)
		}
		if err != nil {
			return err
		}
		u = old
		if up.Role != nil {
			if !up.Role.Valid() {
				return fieldErr("role", "must be admin, operator or readonly")
			}
			u.Role = *up.Role
		}
		if up.Disabled != nil {
			u.Disabled = *up.Disabled
		}
		if old.Role == RoleAdmin && !old.Disabled && (u.Role != RoleAdmin || u.Disabled) {
			n, err := s.otherActiveAdmins(ctx, tx, id)
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.Conflictf("the last enabled admin cannot be demoted or disabled")
			}
		}
		u.UpdatedAt = s.now().UTC().Truncate(time.Second)
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, disabled = ?, updated_at = ? WHERE id = ?`,
			string(u.Role), u.Disabled, u.UpdatedAt.Unix(), id); err != nil {
			return err
		}
		details := map[string]any{
			"before": map[string]any{"role": string(old.Role), "disabled": old.Disabled},
			"after":  map[string]any{"role": string(u.Role), "disabled": u.Disabled},
		}
		if up.Password != nil {
			if err := validatePassword(*up.Password); err != nil {
				return err
			}
			hash, err := HashPassword(*up.Password)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
				return err
			}
			details["password_changed"] = true
			n, err := revokeTokensTx(ctx, tx, id)
			if err != nil {
				return err
			}
			details["tokens_revoked"] = n
		}
		if up.Password != nil || u.Disabled || u.Role != old.Role {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		// Tokens of disabled users stop working via the disabled flag (see
		// LookupToken); they are kept so re-enabling restores access.
		return audit.Insert(ctx, tx, audit.New(ctx, "user.update", "user", fmt.Sprint(id), details))
	})
	return u, err
}

// DeleteUser removes a user (not the last enabled admin).
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("user %d", id)
		}
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && !u.Disabled {
			n, err := s.otherActiveAdmins(ctx, tx, id)
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.Conflictf("the last enabled admin cannot be deleted")
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "user.delete", "user", fmt.Sprint(id), map[string]any{"username": u.Username}))
	})
}

// ---- login / sessions ----------------------------------------------------------

// hashWait bounds how long a login waits for a free argon2id slot before
// it is rejected with ErrBusy.
const hashWait = 5 * time.Second

// Authenticate verifies username and password of an enabled user. It
// returns ErrBusy when the password checks are saturated.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	ctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	var hash string
	var u User
	var role string
	var created, updated int64
	err := s.db.R.QueryRowContext(ctx, `SELECT id, username, role, disabled, created_at, updated_at, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &role, &u.Disabled, &created, &updated, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := VerifyPasswordContext(ctx, dummyHash, password); errors.Is(err, ErrBusy) { // equalise timing
			return User{}, err
		}
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	ok, err := VerifyPasswordContext(ctx, hash, password)
	if err != nil {
		return User{}, err
	}
	if !ok || u.Disabled {
		return User{}, ErrInvalidCredentials
	}
	u.Role = Role(role)
	u.CreatedAt, u.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return u, nil
}

// CreateSession starts a session and returns the raw cookie value.
func (s *Service) CreateSession(ctx context.Context, u User, remoteAddr, userAgent string) (string, Session, error) {
	raw := randomToken(32)
	sess := Session{User: u, CSRFToken: randomToken(24), idHash: hashSecret(raw)}
	now := s.now().UTC().Truncate(time.Second)
	sess.ExpiresAt = now.Add(s.cfg.SessionTTL)
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Opportunistic cleanup of expired sessions.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`,
			now.Unix(), now.Add(-s.cfg.IdleTimeout).Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, remote_addr, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sess.idHash, u.ID, sess.CSRFToken, now.Unix(), sess.ExpiresAt.Unix(), now.Unix(), remoteAddr, userAgent)
		if err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(audit.WithActor(ctx, u.Username, remoteAddr), "auth.login", "user", fmt.Sprint(u.ID), nil))
	})
	return raw, sess, err
}

// LookupSession resolves a cookie value. Expired or idle sessions and
// sessions of disabled users are rejected.
func (s *Service) LookupSession(ctx context.Context, raw string) (*Session, error) {
	if raw == "" {
		return nil, ErrInvalidCredentials
	}
	idHash := hashSecret(raw)
	var (
		sess              Session
		expires, lastSeen int64
	)
	row := s.db.R.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.disabled, u.created_at, u.updated_at, s.csrf_token, s.expires_at, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, idHash)
	var role string
	var created, updated int64
	err := row.Scan(&sess.User.ID, &sess.User.Username, &role, &sess.User.Disabled, &created, &updated, &sess.CSRFToken, &expires, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	sess.User.Role = Role(role)
	sess.User.CreatedAt, sess.User.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	sess.ExpiresAt = time.Unix(expires, 0).UTC()
	sess.idHash = idHash
	now := s.now()
	if sess.User.Disabled || !sess.ExpiresAt.After(now) || now.Sub(time.Unix(lastSeen, 0)) >= s.cfg.IdleTimeout {
		return nil, ErrInvalidCredentials
	}
	// Throttled activity update (at most once a minute per session).
	if now.Sub(time.Unix(lastSeen, 0)) >= time.Minute {
		_ = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now.Unix(), idHash)
			return err
		})
	}
	return &sess, nil
}

// DeleteSession ends a session (logout).
func (s *Service) DeleteSession(ctx context.Context, sess *Session) error {
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, sess.idHash); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "auth.logout", "user", fmt.Sprint(sess.User.ID), nil))
	})
}

// RecordLoginFailure writes an audit entry for a failed login.
func (s *Service) RecordLoginFailure(ctx context.Context, username, reason string) {
	if len(username) > 64 {
		username = username[:64]
	}
	_ = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		return audit.Insert(ctx, tx, audit.New(audit.WithActor(ctx, "anonymous", sourceOf(ctx)), "auth.login_failed", "user", username,
			map[string]any{"reason": reason}))
	})
}

// ChangePassword changes the own password after verifying the current one
// and ends all other sessions of the user.
func (s *Service) ChangePassword(ctx context.Context, sess *Session, userID int64, current, next string) error {
	var hash, username string
	if err := s.db.R.QueryRowContext(ctx, `SELECT username, password_hash FROM users WHERE id = ?`, userID).Scan(&username, &hash); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	ok, err := VerifyPasswordContext(vctx, hash, current)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("current password is wrong: %w", ErrForbidden)
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, newHash, s.now().Unix(), userID); err != nil {
			return err
		}
		keep := ""
		if sess != nil {
			keep = sess.idHash
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keep); err != nil {
			return err
		}
		n, err := revokeTokensTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "user.password", "user", fmt.Sprint(userID), map[string]any{"tokens_revoked": n}))
	})
}

// ---- API tokens ------------------------------------------------------------------

// revokeTokensTx deletes all API tokens of a user. A password change is the
// usual reaction to a suspected compromise, so tokens issued under the old
// password must not survive it.
func revokeTokensTx(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TokenPrefix marks leasorix API tokens.
const TokenPrefix = "lxt_"

// CreateToken issues an API token for user. role may not exceed the user's
// role; empty means the user's role. The raw token is returned once.
func (s *Service) CreateToken(ctx context.Context, u User, name string, role Role, expiresIn time.Duration) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", Token{}, fieldErr("name", "must be 1-128 characters")
	}
	if role == "" {
		role = u.Role
	}
	if !role.Valid() || !u.Role.AtLeast(role) {
		return "", Token{}, fieldErr("role", "must not exceed your own role")
	}
	raw := TokenPrefix + randomToken(32)
	now := s.now().UTC().Truncate(time.Second)
	t := Token{UserID: u.ID, Name: name, Prefix: raw[:len(TokenPrefix)+6], Role: role, CreatedAt: now}
	var expires any
	if expiresIn > 0 {
		e := now.Add(expiresIn)
		t.ExpiresAt = &e
		expires = e.Unix()
	}
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			u.ID, name, hashSecret(raw), t.Prefix, string(role), now.Unix(), expires)
		if err != nil {
			return err
		}
		if t.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "token.create", "api_token", fmt.Sprint(t.ID),
			map[string]any{"name": name, "role": string(role), "user": u.Username}))
	})
	return raw, t, err
}

// ListTokens returns the tokens of a user.
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, user_id, name, prefix, role, created_at, expires_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var role string
		var created int64
		var expires, used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &role, &created, &expires, &used); err != nil {
			return nil, err
		}
		t.Role, t.CreatedAt = Role(role), time.Unix(created, 0).UTC()
		if expires.Valid {
			e := time.Unix(expires.Int64, 0).UTC()
			t.ExpiresAt = &e
		}
		if used.Valid {
			u := time.Unix(used.Int64, 0).UTC()
			t.LastUsedAt = &u
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token of the user.
func (s *Service) DeleteToken(ctx context.Context, userID, id int64) error {
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("token %d", id)
		}
		return audit.Insert(ctx, tx, audit.New(ctx, "token.delete", "api_token", fmt.Sprint(id), nil))
	})
}

// LookupToken resolves a raw API token.
func (s *Service) LookupToken(ctx context.Context, raw string) (*Principal, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return nil, ErrInvalidCredentials
	}
	var (
		t                Token
		u                User
		role, userRole   string
		created          int64
		expires, used    sql.NullInt64
		uCreated, uUpdtd int64
	)
	err := s.db.R.QueryRowContext(ctx, `
		SELECT t.id, t.user_id, t.name, t.prefix, t.role, t.created_at, t.expires_at, t.last_used_at,
		       u.id, u.username, u.role, u.disabled, u.created_at, u.updated_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, hashSecret(raw)).
		Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &role, &created, &expires, &used,
			&u.ID, &u.Username, &userRole, &u.Disabled, &uCreated, &uUpdtd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	t.Role, u.Role = Role(role), Role(userRole)
	if u.Disabled || (expires.Valid && !time.Unix(expires.Int64, 0).After(now)) {
		return nil, ErrInvalidCredentials
	}
	// Effective role: the lower of token and (current) user role.
	eff := t.Role
	if !u.Role.AtLeast(eff) {
		eff = u.Role
	}
	if !used.Valid || now.Sub(time.Unix(used.Int64, 0)) >= time.Minute {
		_ = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.Unix(), t.ID)
			return err
		})
	}
	return &Principal{User: u, Role: eff, Token: &t}, nil
}

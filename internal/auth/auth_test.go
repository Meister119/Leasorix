package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newService(t *testing.T) (*Service, *clock, *store.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Now().UTC().Truncate(time.Second)}
	s := NewService(db, Config{SessionTTL: 8 * time.Hour, IdleTimeout: 30 * time.Minute})
	s.SetClock(c.now)
	return s, c, db
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected format %s", h)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Fatal("valid password rejected")
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$aa$bb", "$argon2id$v=19$m=x$aa$bb", "$argon2id$v=19$m=1,t=1,p=1$!!$bb"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestSetup(t *testing.T) {
	s, _, db := newService(t)
	ctx := context.Background()
	if req, _ := s.SetupRequired(ctx); !req {
		t.Fatal("setup should be required")
	}
	tok := s.SetupToken()
	if tok == "" || s.SetupToken() != tok {
		t.Fatal("setup token not stable")
	}
	if _, err := s.CompleteSetup(ctx, "wrong", "admin", "0123456789"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := s.CompleteSetup(ctx, tok, "admin", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	u, err := s.CompleteSetup(ctx, tok, "admin", "0123456789")
	if err != nil || u.Role != RoleAdmin {
		t.Fatalf("setup: %+v %v", u, err)
	}
	if req, _ := s.SetupRequired(ctx); req {
		t.Fatal("setup still required")
	}
	// Token is one-time; a new one is generated but setup is done anyway.
	if _, err := s.CompleteSetup(ctx, s.SetupToken(), "admin2", "0123456789"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("second setup: %v", err)
	}
	entries, _ := audit.NewLog(db).List(ctx, audit.Filter{Action: "auth.setup"})
	if len(entries) != 1 || entries[0].Actor != "admin" {
		t.Fatalf("setup audit %+v", entries)
	}
}

func TestPresetSetupToken(t *testing.T) {
	s, _, _ := newService(t)
	s.cfg.SetupToken = "preset-token"
	if s.SetupToken() != "preset-token" {
		t.Fatal("preset token ignored")
	}
}

func TestUsersAndLastAdmin(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, "admin", "0123456789", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, pw string }{{"", "0123456789"}, {"has space", "0123456789"}, {"ok", "short"}} {
		if _, err := s.CreateUser(ctx, c.name, c.pw, RoleReadonly); err == nil {
			t.Errorf("user %q accepted", c.name)
		}
	}
	if _, err := s.CreateUser(ctx, "x", "0123456789", "root"); err == nil {
		t.Error("invalid role accepted")
	}
	if _, err := s.CreateUser(ctx, "ADMIN", "0123456789", RoleReadonly); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("case-insensitive duplicate: %v", err)
	}
	ro := RoleReadonly
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Role: &ro}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("last admin demoted: %v", err)
	}
	yes := true
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Disabled: &yes}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("last admin disabled: %v", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("last admin deleted: %v", err)
	}
	second, err := s.CreateUser(ctx, "second", "0123456789", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdate{Role: &ro}); err != nil {
		t.Fatalf("demotion with another admin present: %v", err)
	}
	if err := s.DeleteUser(ctx, second.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Fatal("now second is the last admin")
	}
	users, _ := s.ListUsers(ctx)
	if len(users) != 2 {
		t.Fatalf("users %+v", users)
	}
}

func TestLoginSessions(t *testing.T) {
	s, c, _ := newService(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "op", "0123456789", RoleOperator)
	if _, err := s.Authenticate(ctx, "op", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.Authenticate(ctx, "nobody", "0123456789"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("unknown user accepted")
	}
	got, err := s.Authenticate(ctx, "op", "0123456789")
	if err != nil || got.ID != u.ID {
		t.Fatal(err)
	}
	raw, sess, err := s.CreateSession(ctx, got, "192.0.2.1", "test")
	if err != nil || sess.CSRFToken == "" {
		t.Fatal(err)
	}
	if strings.Contains(raw, sess.idHash) {
		t.Fatal("raw token equals stored hash")
	}
	if l, err := s.LookupSession(ctx, raw); err != nil || l.User.ID != u.ID || l.CSRFToken != sess.CSRFToken {
		t.Fatalf("lookup: %+v %v", l, err)
	}
	if _, err := s.LookupSession(ctx, "bogus"); err == nil {
		t.Fatal("bogus session accepted")
	}
	// Idle timeout.
	c.add(31 * time.Minute)
	if _, err := s.LookupSession(ctx, raw); err == nil {
		t.Fatal("idle session accepted")
	}
	// Activity keeps a session alive until the absolute TTL.
	raw, _, _ = s.CreateSession(ctx, got, "", "")
	for range 30 { // 10 hours of activity every 20 minutes
		c.add(20 * time.Minute)
		if _, err := s.LookupSession(ctx, raw); err != nil {
			break
		}
	}
	if _, err := s.LookupSession(ctx, raw); err == nil {
		t.Fatal("session outlived the absolute TTL")
	}
	// Disabling the user ends sessions.
	raw, sess, _ = s.CreateSession(ctx, got, "", "")
	if _, err := s.CreateUser(ctx, "admin", "0123456789", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := s.UpdateUser(ctx, u.ID, UserUpdate{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, raw); err == nil {
		t.Fatal("session of disabled user accepted")
	}
	if _, err := s.Authenticate(ctx, "op", "0123456789"); err == nil {
		t.Fatal("disabled user can log in")
	}
	_ = sess
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "op", "0123456789", RoleOperator)
	rawA, sessA, _ := s.CreateSession(ctx, u, "", "")
	rawB, _, _ := s.CreateSession(ctx, u, "", "")
	if err := s.ChangePassword(ctx, &sessA, u.ID, "wrong-current", "abcdefghijk"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := s.ChangePassword(ctx, &sessA, u.ID, "0123456789", "abcdefghijk"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, rawA); err != nil {
		t.Fatal("current session ended")
	}
	if _, err := s.LookupSession(ctx, rawB); err == nil {
		t.Fatal("other session survived password change")
	}
	if _, err := s.Authenticate(ctx, "op", "abcdefghijk"); err != nil {
		t.Fatal("new password not active")
	}
}

func TestTokens(t *testing.T) {
	s, c, _ := newService(t)
	ctx := context.Background()
	op, _ := s.CreateUser(ctx, "op", "0123456789", RoleOperator)
	if _, _, err := s.CreateToken(ctx, op, "too-high", RoleAdmin, 0); err == nil {
		t.Fatal("token role above user role accepted")
	}
	raw, tok, err := s.CreateToken(ctx, op, "ci", RoleReadonly, 24*time.Hour)
	if err != nil || !strings.HasPrefix(raw, TokenPrefix) || !strings.HasPrefix(raw, tok.Prefix) {
		t.Fatalf("token %q %+v %v", raw, tok, err)
	}
	p, err := s.LookupToken(ctx, raw)
	if err != nil || p.Role != RoleReadonly || p.User.ID != op.ID {
		t.Fatalf("lookup: %+v %v", p, err)
	}
	if _, err := s.LookupToken(ctx, raw+"x"); err == nil {
		t.Fatal("modified token accepted")
	}
	list, _ := s.ListTokens(ctx, op.ID)
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("list %+v", list)
	}
	// Expiry.
	c.add(25 * time.Hour)
	if _, err := s.LookupToken(ctx, raw); err == nil {
		t.Fatal("expired token accepted")
	}
	// Effective role follows a demoted user.
	raw2, _, _ := s.CreateToken(ctx, op, "full", "", 0)
	if _, err := s.CreateUser(ctx, "admin", "0123456789", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	ro := RoleReadonly
	if _, err := s.UpdateUser(ctx, op.ID, UserUpdate{Role: &ro}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.LookupToken(ctx, raw2); p == nil || p.Role != RoleReadonly {
		t.Fatalf("effective role not capped: %+v", p)
	}
	if err := s.DeleteToken(ctx, op.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, op.ID+99, tok.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("foreign token deleted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for range 3 {
		if b, _ := l.Blocked("k"); b {
			t.Fatal("blocked too early")
		}
		l.Fail("k")
	}
	b, wait := l.Blocked("k")
	if !b || wait != time.Minute {
		t.Fatalf("blocked=%v wait=%v", b, wait)
	}
	now = now.Add(61 * time.Second)
	if b, _ := l.Blocked("k"); b {
		t.Fatal("window did not slide")
	}
	l.Fail("k")
	l.Reset("k")
	if b, _ := l.Blocked("k"); b {
		t.Fatal("reset ignored")
	}
}

func TestRoles(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleOperator) || RoleReadonly.AtLeast(RoleOperator) || !RoleOperator.AtLeast(RoleOperator) || Role("x").AtLeast(RoleReadonly) {
		t.Fatal("role ordering")
	}
}

func TestHashSlotsBoundConcurrency(t *testing.T) {
	// Occupy all slots: a password check must give up with ErrBusy once its
	// context ends instead of starting another 64 MiB argon2id run.
	for i := 0; i < maxConcurrentHashes; i++ {
		hashSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := VerifyPasswordContext(ctx, dummyHash, "whatever-password")
	for i := 0; i < maxConcurrentHashes; i++ {
		<-hashSlots
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if ok, err := VerifyPassword(dummyHash, "leasorix-dummy-password"); err != nil || !ok {
		t.Fatalf("verify after release: %v %v", ok, err)
	}
}

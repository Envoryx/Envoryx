package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/store"
)

func testService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	svc := NewService(st, Options{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour}, log)
	return svc, st
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("expected match, got %v %v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password!")
	if err != nil || ok {
		t.Fatalf("expected mismatch, got %v %v", ok, err)
	}
	if _, err := HashPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected weak password error, got %v", err)
	}
	if _, err := VerifyPassword("$md5$bogus", "x"); err == nil {
		t.Fatal("expected unsupported hash error")
	}
}

func TestSetupLoginValidateLogout(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	needs, err := svc.NeedsSetup(ctx)
	if err != nil || !needs {
		t.Fatalf("expected setup needed, got %v %v", needs, err)
	}
	if _, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateInitialAdmin(ctx, "admin2", "supersecret123"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second setup must be refused, got %v", err)
	}

	if _, _, err := svc.Login(ctx, "admin", "wrong-password", "127.0.0.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "nobody", "supersecret123", "127.0.0.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials for unknown user, got %v", err)
	}
	token, user, err := svc.Login(ctx, "admin", "supersecret123", "127.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Validate(ctx, token)
	if err != nil || p.UserID != user.ID || p.Username != "admin" {
		t.Fatalf("validate failed: %+v %v", p, err)
	}
	if _, err := svc.Validate(ctx, "garbage"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected unauthenticated, got %v", err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected session gone after logout, got %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	if _, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.Login(ctx, "admin", "supersecret123", "127.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if _, err := svc.Validate(ctx, token); err != nil {
		t.Fatalf("session should still be valid: %v", err)
	}
	now = now.Add(2 * time.Hour) // beyond idle timeout since last touch (30min mark)
	if _, err := svc.Validate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected idle expiry, got %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	if _, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < limiterMaxFailures; i++ {
		if _, _, err := svc.Login(ctx, "admin", "wrong-password", "10.0.0.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: expected invalid credentials, got %v", i, err)
		}
	}
	if _, _, err := svc.Login(ctx, "admin", "supersecret123", "10.0.0.1", "ua"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("expected rate limiting, got %v", err)
	}
	// Different IP but same user is blocked as well (per-user key).
	if _, _, err := svc.Login(ctx, "admin", "supersecret123", "10.0.0.2", "ua"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("expected per-user rate limiting, got %v", err)
	}
}

func TestMiddleware(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	if _, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.Login(ctx, "admin", "supersecret123", "127.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	unauth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	h := svc.Middleware(unauth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok || p.Username != "admin" {
			t.Errorf("principal missing in context: %+v", p)
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with cookie, got %d", rec.Code)
	}

	c := svc.Cookie(req, token)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Name != CookieName || c.Secure {
		t.Fatalf("cookie must be HttpOnly + SameSite=Lax, plain over HTTP: %+v", c)
	}

	// Over HTTPS the session is the __Host- cookie alone: a plain one of the same name
	// may come from another host under the domain and is ignored.
	tlsReq := httptest.NewRequest(http.MethodGet, "https://envoryx.example.net/", nil)
	if c := svc.Cookie(tlsReq, token); c.Name != "__Host-"+CookieName || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie over HTTPS: %+v", c)
	}
	tlsReq.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, tlsReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a plain cookie over HTTPS must not sign in: %d", rec.Code)
	}
	tlsReq = httptest.NewRequest(http.MethodGet, "https://envoryx.example.net/", nil)
	tlsReq.AddCookie(&http.Cookie{Name: "__Host-" + CookieName, Value: token})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, tlsReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("the __Host- cookie over HTTPS must sign in: %d", rec.Code)
	}

	// A bearer API token authenticates without a cookie and is marked as such.
	p, _ := svc.Validate(ctx, token)
	secret, _, err := svc.CreateAPIToken(ctx, p, TokenSpec{Name: "cli", Scope: ScopeOperate, Projects: []string{"p2", "p1", "p2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAPIToken(ctx, p, TokenSpec{Name: "bad", Scope: "root"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unknown scope = %v", err)
	}
	// A scope above the role names the limit without calling the scope invalid.
	if _, _, err := svc.CreateAPIToken(ctx, Principal{UserID: p.UserID, Role: "viewer"}, TokenSpec{Name: "big", Scope: ScopeAdmin}); !errors.Is(err, ErrInvalidScope) || err.Error() != "your role allows at most read tokens" {
		t.Fatalf("scope above the role = %v", err)
	}
	if _, _, err := svc.CreateAPIToken(ctx, Principal{UserID: p.UserID, Role: "none"}, TokenSpec{Name: "any", Scope: ScopeRead}); !errors.Is(err, ErrInvalidScope) || err.Error() != "your role allows no API tokens" {
		t.Fatalf("no role = %v", err)
	}
	var seen Principal
	hb := svc.Middleware(unauth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec = httptest.NewRecorder()
	hb.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen.Username != "admin" || seen.TokenName != "cli" || seen.SessionID != "" {
		t.Fatalf("bearer: %d %+v", rec.Code, seen)
	}
	if seen.Scope != ScopeOperate || strings.Join(seen.Projects, ",") != "p1,p2" {
		t.Fatalf("scope/projects on the principal: %+v", seen)
	}
	// An admin's session may do everything; tokens are held to their scope and projects,
	// and a token confined to projects does nothing instance-wide.
	if !p.Allows(ScopeAdmin) || p.Restricted() || p.Confined() {
		t.Fatalf("an admin's session must be unrestricted: %+v", p)
	}
	if !seen.Confined() || seen.Allows(ScopeRead) {
		t.Fatalf("a confined token has no instance access: %+v", seen)
	}
	if !seen.CanAccessProject("p1") || seen.CanAccessProject("p3") || seen.Require(ScopeRead, "p3") == nil || seen.Require(ScopeAdmin, "p1") == nil || seen.Require(ScopeOperate, "p2") != nil {
		t.Fatalf("project restriction not enforced: %+v", seen)
	}
	if (Principal{TokenName: "legacy"}).Allows(ScopeRead) {
		t.Fatal("a token without a scope must cover nothing")
	}

	// An invalid bearer token is rejected even when a valid cookie is present.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer stq_definitelynotatoken0000000000")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec = httptest.NewRecorder()
	hb.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer must not fall back to the cookie: %d", rec.Code)
	}
}

func TestRolesAndScopes(t *testing.T) {
	viewer := Principal{Role: "viewer", ProjectRoles: map[string]Role{"shop": RoleDeveloper, "prod": RoleNone}}
	if viewer.ScopeFor("") != ScopeRead || viewer.ScopeFor("blog") != ScopeRead || viewer.ScopeFor("shop") != ScopeOperate || viewer.ScopeFor("prod") != "" {
		t.Fatalf("viewer scopes: %s %s %s %q", viewer.ScopeFor(""), viewer.ScopeFor("blog"), viewer.ScopeFor("shop"), viewer.ScopeFor("prod"))
	}
	if viewer.Allows(ScopeOperate) || !viewer.Allows(ScopeRead) || viewer.Confined() {
		t.Fatal("a viewer reads the instance and operates nothing instance-wide")
	}
	if viewer.Require(ScopeOperate, "shop") != nil || viewer.Require(ScopeOperate, "blog") == nil || viewer.CanAccessProject("prod") {
		t.Fatal("project roles replace the global role")
	}
	// A token of that viewer never exceeds the user, even with admin scope.
	tok := viewer
	tok.TokenName, tok.Scope = "ci", ScopeAdmin
	if tok.ScopeFor("shop") != ScopeOperate || tok.ScopeFor("blog") != ScopeRead {
		t.Fatalf("token capped by the user: %s %s", tok.ScopeFor("shop"), tok.ScopeFor("blog"))
	}
	tok.Scope = ScopeRead
	if tok.ScopeFor("shop") != ScopeRead {
		t.Fatal("and by its own scope")
	}
	// A user with no global role only reaches the projects given to them.
	none := Principal{Role: "none", ProjectRoles: map[string]Role{"shop": RoleViewer}}
	if !none.Confined() || none.Allows(ScopeRead) || !none.CanAccessProject("shop") || none.CanAccessProject("blog") {
		t.Fatal("role none is confined to its projects")
	}
	// On the instance a confined principal hears about the project restriction only when
	// its scope would do; otherwise the scope is what falls short.
	confined := Principal{Role: "admin", TokenName: "ci", Scope: ScopeRead, Projects: []string{"shop"}}
	if err := confined.Require(ScopeOperate, ""); err == nil || !strings.Contains(err.Error(), "this token has read scope, the operation needs operate") {
		t.Fatalf("confined read token asking for operate: %v", err)
	}
	confined.Scope = ScopeOperate
	if err := confined.Require(ScopeOperate, ""); err == nil || !strings.Contains(err.Error(), "limited to particular projects") {
		t.Fatalf("confined operate token on the instance: %v", err)
	}
	// An admin stays admin everywhere, whatever the project roles say.
	admin := Principal{Role: "admin", ProjectRoles: map[string]Role{"shop": RoleViewer}}
	if admin.ScopeFor("shop") != ScopeAdmin {
		t.Fatal("admin everywhere")
	}
	if _, err := ParseRole("owner"); err == nil {
		t.Fatal("unknown roles are refused")
	}
}

// A token with an expiry stops signing in once it is reached; one without works on.
func TestAPITokenExpiry(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	if _, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	session, _, err := svc.Login(ctx, "admin", "supersecret123", "127.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Validate(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []time.Duration{-time.Hour, time.Minute, MaxTokenLifetime + time.Hour} {
		if _, _, err := svc.CreateAPIToken(ctx, p, TokenSpec{Name: "x", Scope: ScopeRead, ExpiresIn: bad}); !errors.Is(err, ErrInvalidTokenExpiry) {
			t.Fatalf("expiry %v = %v", bad, err)
		}
	}
	short, tok, err := svc.CreateAPIToken(ctx, p, TokenSpec{Name: "ci", Scope: ScopeRead, ExpiresIn: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("expires at %v", tok.ExpiresAt)
	}
	forever, _, err := svc.CreateAPIToken(ctx, p, TokenSpec{Name: "cron", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30*24*time.Hour - time.Second)
	if _, err := svc.ValidateAPIToken(ctx, short); err != nil {
		t.Fatalf("token before its expiry: %v", err)
	}
	now = now.Add(time.Second)
	if _, err := svc.ValidateAPIToken(ctx, short); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token = %v", err)
	}
	now = now.Add(5 * 365 * 24 * time.Hour)
	if _, err := svc.ValidateAPIToken(ctx, forever); err != nil {
		t.Fatalf("token without expiry: %v", err)
	}
}

// Concurrent setup requests create one admin; the others are told setup is done.
func TestConcurrentSetupCreatesOneAdmin(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.CreateInitialAdmin(ctx, fmt.Sprintf("admin%d", i), "supersecret123")
		}()
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, store.ErrConflict):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	users, err := st.Users.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || len(users) != 1 {
		t.Fatalf("created %d, users %d", created, len(users))
	}
}

// Showing the secret key takes the password, or for an account without one (single
// sign-on) a sign-in within the last minutes.
func TestConfirmIdentity(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	admin, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.StartSession(ctx, admin, MethodPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Validate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmIdentity(ctx, p, "wrong-password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := svc.ConfirmIdentity(ctx, p, "supersecret123"); err != nil {
		t.Fatalf("password: %v", err)
	}

	sso, err := st.Users.Create(ctx, "sso", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	token, err = svc.StartSession(ctx, sso, MethodOIDC, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	p, err = svc.Validate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmIdentity(ctx, p, ""); err != nil {
		t.Fatalf("fresh sign-on: %v", err)
	}
	start := time.Now()
	svc.now = func() time.Time { return start.Add(11 * time.Minute) }
	if err := svc.ConfirmIdentity(ctx, p, ""); !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("an older sign-on: %v", err)
	}
}

// With the shortest idle timeout an active session stays: it is touched more often than
// every minute.
func TestShortIdleTimeoutSlides(t *testing.T) {
	svc, _ := testService(t)
	svc.opts.IdleTimeout, svc.opts.AbsoluteTimeout = time.Minute, time.Hour
	ctx := context.Background()
	user, err := svc.CreateInitialAdmin(ctx, "admin", "supersecret123")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.StartSession(ctx, user, MethodPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 1; i <= 6; i++ {
		at := start.Add(time.Duration(i) * 25 * time.Second)
		svc.now = func() time.Time { return at }
		if _, err := svc.Validate(ctx, token); err != nil {
			t.Fatalf("request at %ds: %v", i*25, err)
		}
	}
}

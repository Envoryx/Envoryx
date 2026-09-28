package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

	c := svc.Cookie(token)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie must be HttpOnly + SameSite=Lax: %+v", c)
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
	// An admin stays admin everywhere, whatever the project roles say.
	admin := Principal{Role: "admin", ProjectRoles: map[string]Role{"shop": RoleViewer}}
	if admin.ScopeFor("shop") != ScopeAdmin {
		t.Fatal("admin everywhere")
	}
	if _, err := ParseRole("owner"); err == nil {
		t.Fatal("unknown roles are refused")
	}
}

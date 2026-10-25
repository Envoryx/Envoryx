package api_test

import (
	"net/http"
	"testing"
)

// A password change keeps the browser that made it signed in and ends every other session.
func TestPasswordChangeSignsOutOtherSessions(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	first := a.cookie
	a.cookie = nil
	r := a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "supersecret123"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("second login: %d %s", r.status, r.raw)
	}
	other := r.cookie

	a.cookie = first
	r = a.do(http.MethodPost, "/api/v1/auth/password", map[string]string{"currentPassword": "supersecret123", "newPassword": "anothersecret123"}, true)
	if r.status != http.StatusNoContent || r.cookie == nil {
		t.Fatalf("change: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie
	if r := a.do(http.MethodGet, "/api/v1/auth/me", nil, false); r.status != http.StatusOK {
		t.Fatalf("the browser that changed the password must stay signed in: %d", r.status)
	}
	a.cookie = first
	if r := a.do(http.MethodGet, "/api/v1/auth/me", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("its old session cookie must be gone: %d", r.status)
	}
	a.cookie = other
	if r := a.do(http.MethodGet, "/api/v1/auth/me", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("another session must be signed out: %d", r.status)
	}
}

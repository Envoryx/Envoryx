package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// Over HTTPS the session lives in a __Host- cookie, and a plain one of the old name,
// as another host under the domain could set it, signs nobody in.
func TestSessionCookieOverHTTPS(t *testing.T) {
	a := newApp(t)
	srv := httptest.NewTLSServer(a.srv.Config.Handler)
	defer srv.Close()
	client := srv.Client()
	call := func(method, path, body string, c *http.Cookie) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "Envoryx")
		if c != nil {
			req.AddCookie(c)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	res := call(http.MethodPost, "/api/v1/setup", `{"username":"admin","password":"supersecret123"}`, nil)
	var session *http.Cookie
	for _, c := range res.Cookies() {
		session = c
	}
	if res.StatusCode != http.StatusCreated || session == nil || session.Name != "__Host-envoryx_session" || !session.Secure || session.Path != "/" || session.Domain != "" {
		t.Fatalf("setup over HTTPS: %d %+v", res.StatusCode, session)
	}
	if res := call(http.MethodGet, "/api/v1/auth/me", "", session); res.StatusCode != http.StatusOK {
		t.Fatalf("the __Host- cookie must sign in: %d", res.StatusCode)
	}
	planted := &http.Cookie{Name: "envoryx_session", Value: session.Value}
	if res := call(http.MethodGet, "/api/v1/auth/me", "", planted); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a plain cookie over HTTPS must not sign in: %d", res.StatusCode)
	}
}

package api_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// totpNow is the TOTP code of secret, offset steps from now.
func totpNow(t *testing.T, secret string, offset int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(time.Now().Unix()/30+offset))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// enableTOTP sets up TOTP for the signed-in user and returns the secret and the codes.
func (a *testApp) enableTOTP(password string) (string, []any) {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/auth/2fa/totp/setup", map[string]string{"password": password}, true)
	if r.status != http.StatusOK || !strings.HasPrefix(r.body["qr"].(string), "<svg") {
		a.t.Fatalf("totp setup: %d %s", r.status, r.raw)
	}
	secret := r.body["secret"].(string)
	// A wrong code is a 422, not a 401: the session is fine and the UI must stay in the
	// setup instead of signing the user out. The setup goes on afterwards.
	if r := a.do(http.MethodPost, "/api/v1/auth/2fa/totp", map[string]string{"code": "000000"}, true); r.status != http.StatusUnprocessableEntity || errCode(r) != "invalid_code" {
		a.t.Fatalf("wrong code during the setup: %d %s", r.status, r.raw)
	}
	// The previous step: the test signs in with the current one right after.
	r = a.do(http.MethodPost, "/api/v1/auth/2fa/totp", map[string]string{"code": totpNow(a.t, secret, -1)}, true)
	if r.status != http.StatusOK {
		a.t.Fatalf("totp confirm: %d %s", r.status, r.raw)
	}
	return secret, r.body["recoveryCodes"].([]any)
}

func TestTwoFactorSignIn(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	secret, codes := a.enableTOTP("supersecret123")
	if len(codes) != 10 {
		t.Fatalf("%d recovery codes", len(codes))
	}
	if r := a.do(http.MethodGet, "/api/v1/auth/2fa", nil, true); r.status != http.StatusOK || r.body["totp"] != true || r.body["recoveryCodes"].(float64) != 10 {
		t.Fatalf("status: %d %s", r.status, r.raw)
	}

	// The password alone answers with a ticket, no cookie.
	a.cookie = nil
	r := a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "supersecret123"}, true)
	step, _ := r.body["secondFactor"].(map[string]any)
	if r.status != http.StatusOK || r.cookie != nil || step == nil {
		t.Fatalf("password step: %d %s", r.status, r.raw)
	}
	ticket := step["ticket"].(string)
	if r := a.do(http.MethodPost, "/api/v1/auth/login/code", map[string]string{"ticket": ticket, "code": "000000"}, true); r.status != http.StatusUnauthorized || errCode(r) != "invalid_code" {
		t.Fatalf("wrong code: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/auth/login/code", map[string]string{"ticket": ticket, "code": totpNow(t, secret, 0)}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("code step: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie

	// The audit log names the method.
	r = a.do(http.MethodGet, "/api/v1/audit?action=auth.login", nil, true)
	if !strings.Contains(string(r.raw), `"method":"totp"`) {
		t.Errorf("audit: %s", r.raw)
	}

	// A wrong password to confirm a change doesn't end the session (403, not 401).
	if r := a.do(http.MethodPost, "/api/v1/auth/2fa/recovery-codes", map[string]string{"password": "wrong-password"}, true); r.status != http.StatusForbidden || errCode(r) != "wrong_password" {
		t.Fatalf("wrong password: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/auth/me", nil, true); r.status != http.StatusOK {
		t.Fatalf("session after a wrong password: %d", r.status)
	}

	// A password reset link leads to the second step too.
	r = a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "bob", "role": "developer"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.status, r.raw)
	}
	bobID := r.body["user"].(map[string]any)["id"].(string)
	url := r.body["inviteUrl"].(string)
	admin := a.cookie
	a.cookie = nil
	r = a.do(http.MethodPost, "/api/v1/invites/"+url[strings.LastIndex(url, "/")+1:], map[string]string{"password": "bob-secret-1234"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("accept: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie
	a.enableTOTP("bob-secret-1234")
	a.cookie = admin
	r = a.do(http.MethodPost, "/api/v1/users/"+bobID+"/invite", nil, true)
	url = r.body["inviteUrl"].(string)
	a.cookie = nil
	r = a.do(http.MethodPost, "/api/v1/invites/"+url[strings.LastIndex(url, "/")+1:], map[string]string{"password": "bob-new-secret-1"}, true)
	if r.status != http.StatusOK || r.cookie != nil || r.body["secondFactor"] == nil {
		t.Fatalf("reset link of a user with TOTP: %d %s", r.status, r.raw)
	}

	// The admin resets Bob's factors, but not their own.
	a.cookie = admin
	if r := a.do(http.MethodGet, "/api/v1/users", nil, true); !strings.Contains(string(r.raw), `"twoFactor":true`) {
		t.Fatalf("users: %s", r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/users/"+bobID+"/two-factor", nil, true); r.status != http.StatusNoContent {
		t.Fatalf("reset: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, true)
	adminID := r.body["user"].(map[string]any)["id"].(string)
	if r := a.do(http.MethodDelete, "/api/v1/users/"+adminID+"/two-factor", nil, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("own reset: %d %s", r.status, r.raw)
	}
	a.cookie = nil
	if r := a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "bob", "password": "bob-new-secret-1"}, true); r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("Bob after the reset: %d %s", r.status, r.raw)
	}
}

func TestTwoFactorRequiredSetup(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	admin := a.cookie
	if r := a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"twoFactorRequired": "sometimes"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a bad level: %d %s", r.status, r.raw)
	}
	// An API token can't change the requirement.
	r := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ci", "scope": "admin"}, true)
	a.bearer, a.cookie = r.body["secret"].(string), nil
	if r := a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"twoFactorRequired": "all"}, true); r.status != http.StatusForbidden {
		t.Fatalf("token sets the requirement: %d %s", r.status, r.raw)
	}
	a.bearer, a.cookie = "", admin
	if r := a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"twoFactorRequired": "admins"}, true); r.status != http.StatusOK || r.body["twoFactorRequired"] != "admins" {
		t.Fatalf("set the requirement: %d %s", r.status, r.raw)
	}
	// The admin's session can now only set up a factor.
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, true)
	if r.body["twoFactorSetup"] != true {
		t.Fatalf("me: %s", r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects", nil, true); r.status != http.StatusForbidden || errCode(r) != "two_factor_setup" {
		t.Fatalf("projects before the setup: %d %s", r.status, r.raw)
	}
	a.enableTOTP("supersecret123")
	if r := a.do(http.MethodGet, "/api/v1/projects", nil, true); r.status != http.StatusOK {
		t.Fatalf("projects after the setup: %d %s", r.status, r.raw)
	}
	// The last required factor stays.
	if r := a.do(http.MethodDelete, "/api/v1/auth/2fa/totp", map[string]string{"password": "supersecret123"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("removing the last required factor: %d %s", r.status, r.raw)
	}
}

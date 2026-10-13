package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/store"
)

// RFC 6238 appendix B, SHA-1: the secret is "12345678901234567890"; the RFC lists 8
// digits, the 6-digit code is their last six.
func TestTOTPVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	for _, v := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(key, v.unix/totpPeriod); got != v.code {
			t.Errorf("code at %d = %s, want %s", v.unix, got, v.code)
		}
		if _, ok := totpMatch(secret, v.code, time.Unix(v.unix, 0)); !ok {
			t.Errorf("%s doesn't match at %d", v.code, v.unix)
		}
		// One step either way is clock drift; two are too many.
		if _, ok := totpMatch(secret, v.code, time.Unix(v.unix+totpPeriod, 0)); !ok {
			t.Errorf("%s doesn't match a step later", v.code)
		}
		if _, ok := totpMatch(secret, v.code, time.Unix(v.unix+3*totpPeriod, 0)); ok {
			t.Errorf("%s matches three steps later", v.code)
		}
	}
	if _, ok := totpMatch(secret, "12345", time.Unix(59, 0)); ok {
		t.Error("a short code matched")
	}
}

// twoFactorUser makes a user with a password and returns the service at a fixed time.
func twoFactorUser(t *testing.T, role string) (*Service, *store.Store, store.User, *time.Time) {
	t.Helper()
	svc, st := testService(t)
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	hash, err := HashPassword("alice-secret-123")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Users.Create(context.Background(), "alice", hash, role)
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, u, &clock
}

func currentCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpCode(key, at.Unix()/totpPeriod)
}

func TestTOTPSignIn(t *testing.T) {
	svc, _, u, clock := twoFactorUser(t, "admin")
	ctx := context.Background()
	p := Principal{UserID: u.ID, Username: u.Username}

	if _, _, err := svc.BeginTOTP(ctx, p, "wrong-password-1"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("setup with a wrong password: %v", err)
	}
	secret, uri, err := svc.BeginTOTP(ctx, p, "alice-secret-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/Envoryx:alice?") || !strings.Contains(uri, "secret="+secret) {
		t.Fatalf("uri %s", uri)
	}
	if _, err := svc.ConfirmTOTP(ctx, p, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("confirm with a wrong code: %v", err)
	}
	codes, err := svc.ConfirmTOTP(ctx, p, currentCode(t, secret, *clock))
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatalf("confirm: %v, %d codes", err, len(codes))
	}

	// The password alone no longer opens a session.
	if _, _, err := svc.Login(ctx, "alice", "alice-secret-123", "1.2.3.4", "test"); !errors.Is(err, ErrSecondFactor) {
		t.Fatalf("Login of a user with TOTP: %v", err)
	}
	token, _, step, err := svc.PasswordLogin(ctx, "alice", "alice-secret-123", "1.2.3.4", "test")
	if err != nil || token != "" || step == nil || strings.Join(step.Methods, ",") != "totp,recovery" {
		t.Fatalf("PasswordLogin: %q %+v %v", token, step, err)
	}
	// The code that confirmed the setup was used: the same step doesn't sign in again.
	if _, _, err := svc.CompleteTOTP(ctx, step.Ticket, currentCode(t, secret, *clock), "1.2.3.4", "test"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("a used code: %v", err)
	}
	*clock = clock.Add(totpPeriod * time.Second)
	token, user, err := svc.CompleteTOTP(ctx, step.Ticket, currentCode(t, secret, *clock), "1.2.3.4", "test")
	if err != nil || token == "" || user.ID != u.ID {
		t.Fatalf("CompleteTOTP: %v", err)
	}
	pr, err := svc.Validate(ctx, token)
	if err != nil || pr.SessionMethod != MethodTOTP || pr.TwoFactorSetup {
		t.Fatalf("session: %+v %v", pr, err)
	}
	// The ticket was used up.
	if _, _, err := svc.CompleteTOTP(ctx, step.Ticket, currentCode(t, secret, *clock), "1.2.3.4", "test"); !errors.Is(err, ErrTicket) {
		t.Fatalf("a used ticket: %v", err)
	}

	// A recovery code works once.
	_, _, step, _ = svc.PasswordLogin(ctx, "alice", "alice-secret-123", "1.2.3.4", "test")
	if _, _, err := svc.CompleteRecovery(ctx, step.Ticket, strings.ToUpper(codes[0]), "1.2.3.4", "test"); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	_, _, step, _ = svc.PasswordLogin(ctx, "alice", "alice-secret-123", "1.2.3.4", "test")
	if _, _, err := svc.CompleteRecovery(ctx, step.Ticket, codes[0], "1.2.3.4", "test"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("a used recovery code: %v", err)
	}

	// Wrong codes end the ticket after ticketTries.
	_, _, step, _ = svc.PasswordLogin(ctx, "alice", "alice-secret-123", "5.6.7.8", "test")
	for i := 0; i < ticketTries; i++ {
		_, _, _ = svc.CompleteTOTP(ctx, step.Ticket, "000000", "5.6.7.8", "test")
	}
	if _, _, err := svc.CompleteTOTP(ctx, step.Ticket, currentCode(t, secret, clock.Add(time.Minute)), "5.6.7.8", "test"); !errors.Is(err, ErrTicket) && !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("a ticket after %d wrong codes: %v", ticketTries, err)
	}

	// Removing TOTP drops the recovery codes with it.
	if err := svc.RemoveTOTP(ctx, p, "alice-secret-123"); err != nil {
		t.Fatal(err)
	}
	st, err := svc.TwoFactorStatus(ctx, p)
	if err != nil || st.TOTP || st.RecoveryCodes != 0 {
		t.Fatalf("after removing: %+v %v", st, err)
	}
	if _, _, err := svc.Login(ctx, "alice", "alice-secret-123", "1.2.3.4", "test"); err != nil {
		t.Fatalf("Login without a factor: %v", err)
	}
}

func TestTwoFactorRequirement(t *testing.T) {
	svc, st, u, clock := twoFactorUser(t, "admin")
	ctx := context.Background()
	viewer, err := st.Users.Create(ctx, "victor", u.PasswordHash, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	sso, err := st.Users.Insert(ctx, store.User{Username: "sam", Role: "admin", OIDCSubject: "sub-1"})
	if err != nil {
		t.Fatal(err)
	}
	session := func(user store.User, method string) Principal {
		t.Helper()
		token, err := svc.StartSession(ctx, user, method, "1.2.3.4", "test")
		if err != nil {
			t.Fatal(err)
		}
		p, err := svc.Validate(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if session(u, MethodPassword).TwoFactorSetup {
		t.Fatal("no requirement, yet the session has to set up a factor")
	}
	if err := svc.SetRequirement(ctx, RequireAdmins); err != nil {
		t.Fatal(err)
	}
	if !session(u, MethodPassword).TwoFactorSetup || !session(u, "").TwoFactorSetup {
		t.Fatal("an admin's password session isn't held to the requirement")
	}
	if session(viewer, MethodPassword).TwoFactorSetup {
		t.Fatal("a viewer is held to the admins' requirement")
	}
	if session(sso, MethodOIDC).TwoFactorSetup || session(sso, "").TwoFactorSetup {
		t.Fatal("single sign-on is held to the requirement")
	}
	if err := svc.SetRequirement(ctx, RequireAll); err != nil {
		t.Fatal(err)
	}
	if !session(viewer, MethodInvite).TwoFactorSetup {
		t.Fatal("a viewer isn't held to the requirement of everyone")
	}

	// Setting up a factor lifts it; the last factor can't go then.
	p := Principal{UserID: u.ID, Username: u.Username}
	secret, _, err := svc.BeginTOTP(ctx, p, "alice-secret-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmTOTP(ctx, p, currentCode(t, secret, *clock)); err != nil {
		t.Fatal(err)
	}
	if session(u, MethodPassword).TwoFactorSetup {
		t.Fatal("the requirement holds after a factor was set up")
	}
	if err := svc.RemoveTOTP(ctx, p, "alice-secret-123"); !errors.Is(err, ErrFactorRequired) {
		t.Fatalf("removing the last required factor: %v", err)
	}

	// An admin resets it for someone else, not for themselves.
	if _, err := svc.ResetTwoFactor(ctx, p, u.ID); err == nil {
		t.Fatal("an admin reset their own factors")
	}
	if _, err := svc.ResetTwoFactor(ctx, Principal{UserID: viewer.ID}, u.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.MFA.Factors(ctx, u.ID); f.Any() || f.RecoveryCodes != 0 {
		t.Fatalf("after the reset: %+v", f)
	}
	// A user without a password has nothing to protect with a second factor.
	if _, _, err := svc.BeginTOTP(ctx, Principal{UserID: sso.ID}, ""); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("TOTP for a single sign-on user: %v", err)
	}
}

func TestRecoveryCodeHashIgnoresFormatting(t *testing.T) {
	if hashRecoveryCode("abcde-fghij") != hashRecoveryCode(" ABCDE FGHIJ ") {
		t.Fatal("the same code typed differently hashes differently")
	}
	if hashRecoveryCode("abcde-fghij") == hashRecoveryCode("abcde-fghik") {
		t.Fatal("two codes hash alike")
	}
}

func TestRelyingPartyFor(t *testing.T) {
	for origin, want := range map[string]string{
		"https://envoryx.example.com":      "envoryx.example.com",
		"https://Envoryx.Example.com:8443": "envoryx.example.com",
		"http://localhost:5173":            "localhost",
		"http://envoryx.example.com":       "",
		"https://192.168.1.10:8787":        "",
		"https://[::1]":                    "",
		"https://unraid":                   "unraid",
		"":                                 "",
	} {
		rp, err := RelyingPartyFor(origin)
		if want == "" {
			if err == nil {
				t.Errorf("%q gave %+v", origin, rp)
			}
			continue
		}
		if err != nil || rp.ID != want {
			t.Errorf("%q: %+v %v, want %s", origin, rp, err, want)
		}
	}
}

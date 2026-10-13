package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/store"
)

func newAdminCLI(t *testing.T) (*adminCLI, *store.Store, *bytes.Buffer) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	out := &bytes.Buffer{}
	return &adminCLI{store: st, audit: audit.New(st.Audit, log), out: out, now: time.Now}, st, out
}

func seedUser(t *testing.T, st *store.Store, name, password string) store.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Users.Create(context.Background(), name, hash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAdminResetPasswordGeneratesAndEndsSessions(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	u := seedUser(t, st, "stefan", "old-password-1")
	if err := st.Sessions.Create(ctx, store.Session{ID: "s1", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := cli.run(ctx, []string{"reset-password"}); err != nil {
		t.Fatal(err)
	}
	// The generated password is the indented line of the output.
	var generated string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "  ") {
			generated = strings.TrimSpace(line)
		}
	}
	if len(generated) != 20 {
		t.Fatalf("expected a 20 character password in output, got %q\n%s", generated, out.String())
	}
	fresh, err := st.Users.ByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth.VerifyPassword(fresh.PasswordHash, generated); !ok {
		t.Fatal("generated password does not verify")
	}
	if ok, _ := auth.VerifyPassword(fresh.PasswordHash, "old-password-1"); ok {
		t.Fatal("old password still valid")
	}
	if _, err := st.Sessions.Get(ctx, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session should be gone, got %v", err)
	}
	entries, _ := st.Audit.Recent(ctx, 5)
	if len(entries) != 1 || entries[0].Action != audit.ActionPasswordReset || entries[0].Username != "cli" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestAdminResetPasswordExplicit(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	seedUser(t, st, "a", "password-a-1")
	seedUser(t, st, "b", "password-b-1")

	err := cli.run(ctx, []string{"reset-password"})
	if err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("expected an error asking for --user with two accounts, got %v", err)
	}
	if err := cli.run(ctx, []string{"reset-password", "--user", "B", "--password", "brand-new-pw-2"}); err != nil {
		t.Fatal(err)
	}
	b, _ := st.Users.ByUsername(ctx, "b")
	if ok, _ := auth.VerifyPassword(b.PasswordHash, "brand-new-pw-2"); !ok {
		t.Fatal("explicit password not applied")
	}
	if !strings.Contains(out.String(), "Password of b changed") {
		t.Fatalf("unexpected output %q", out.String())
	}
	if err := cli.run(ctx, []string{"reset-password", "--password", "short"}); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("policy check must come after account selection, got %v", err)
	}
	if err := cli.run(ctx, []string{"reset-password", "--user", "a", "--password", "short"}); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("expected the password policy error, got %v", err)
	}
	if err := cli.run(ctx, []string{"reset-password", "--user", "nobody"}); err == nil || !strings.Contains(err.Error(), "no account named") {
		t.Fatalf("expected unknown account error, got %v", err)
	}
}

func TestAdminLogoutAllRevokeTokensAndReset(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	u := seedUser(t, st, "stefan", "old-password-1")
	for _, id := range []string{"s1", "s2"} {
		if err := st.Sessions.Create(ctx, store.Session{ID: id, UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Tokens.Create(ctx, store.APIToken{ID: "t1", UserID: u.ID, Name: "laptop", TokenHash: "h1", Prefix: "p1", Scope: "admin", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := cli.run(ctx, []string{"logout-all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 session(s) ended") {
		t.Fatalf("unexpected output %q", out.String())
	}
	out.Reset()
	if err := cli.run(ctx, []string{"revoke-tokens"}); err != nil {
		t.Fatal(err)
	}
	if toks, _ := st.Tokens.List(ctx); len(toks) != 0 {
		t.Fatalf("tokens still present: %+v", toks)
	}
	// What a token holder may have opened besides is named, not cleared.
	if !strings.Contains(out.String(), "SSH keys") || !strings.Contains(out.String(), "single sign-on") {
		t.Fatalf("revoke-tokens names no SSH keys and single sign-on: %q", out.String())
	}

	if err := cli.run(ctx, []string{"reset"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("reset without --yes must refuse, got %v", err)
	}
	out.Reset()
	if err := cli.run(ctx, []string{"reset", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SSH keys") || !strings.Contains(out.String(), "single sign-on") {
		t.Fatalf("reset names no SSH keys and single sign-on: %q", out.String())
	}
	if n, _ := st.Users.Count(ctx); n != 0 {
		t.Fatalf("expected no users, got %d", n)
	}
	out.Reset()
	if err := cli.run(ctx, []string{"users"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No accounts") {
		t.Fatalf("unexpected output %q", out.String())
	}
	entries, _ := st.Audit.Recent(ctx, 10)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	want := []string{audit.ActionAccountsReset, audit.ActionTokensRevoked, audit.ActionSessionsRevoked}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions %v, want %v", actions, want)
	}
}

func TestAdminUsageErrors(t *testing.T) {
	cli, _, _ := newAdminCLI(t)
	ctx := context.Background()
	for _, args := range [][]string{{"bogus"}, {"reset-password", "extra"}, {"reset", "--nope"}} {
		if err := cli.run(ctx, args); !errors.Is(err, errAdminUsage) {
			t.Errorf("%v: expected usage error, got %v", args, err)
		}
	}
}

// Secrets on the command line end up in the shell history and the process list: the
// usage texts say so where they take one.
func TestUsageWarnsOfSecretsOnTheCommandLine(t *testing.T) {
	for name, text := range map[string]string{"admin": adminUsage, "cli": cliUsage} {
		if !strings.Contains(text, "shell history") || !strings.Contains(text, "ps") {
			t.Errorf("%s usage doesn't warn of secrets on the command line", name)
		}
	}
	if !strings.Contains(cliUsage, `prefer "-"`) || !strings.Contains(adminUsage, "prefer the generated one") {
		t.Error("the usage texts recommend no way around it")
	}
}

func TestAdminResetTwoFactor(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	u := seedUser(t, st, "stefan", "old-password-1")
	if err := st.MFA.SetTOTP(ctx, u.ID, "JBSWY3DPEHPK3PXP", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.MFA.SetRecoveryCodes(ctx, u.ID, []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := cli.run(ctx, []string{"users"}); err != nil || !strings.Contains(out.String(), "2FA") || !strings.Contains(out.String(), "yes") {
		t.Fatalf("users: %v\n%s", err, out.String())
	}
	if err := cli.run(ctx, []string{"reset-2fa", "--user", "stefan"}); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.MFA.Factors(ctx, u.ID); f.Any() || f.RecoveryCodes != 0 {
		t.Fatalf("factors after the reset: %+v", f)
	}
	entries, _ := st.Audit.Recent(ctx, 5)
	if len(entries) != 1 || entries[0].Action != audit.ActionTwoFactorReset || entries[0].Username != "cli" {
		t.Fatalf("audit: %+v", entries)
	}
}

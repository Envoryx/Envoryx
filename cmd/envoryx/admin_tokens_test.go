package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

func TestAdminCreateTokenWorksForTheAPI(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	seedUser(t, st, "stefan", "password-1234")
	shop := &store.Project{Name: "Acme Shop", Slug: "acme-shop", Path: "acme-shop"}
	if err := st.Projects.Create(ctx, shop); err != nil {
		t.Fatal(err)
	}
	if err := cli.run(ctx, []string{"create-token", "--name", "ci", "--scope", "read", "--project", "Acme Shop", "--quiet"}); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(out.String())
	p, err := auth.NewService(st, auth.Options{}, nil).ValidateAPIToken(ctx, secret)
	if err != nil {
		t.Fatalf("the printed secret is no valid token: %v", err)
	}
	if p.TokenName != "ci" || p.Scope != auth.ScopeRead || len(p.Projects) != 1 || p.Projects[0] != shop.ID {
		t.Fatalf("token principal %+v", p)
	}

	out.Reset()
	if err := cli.run(ctx, []string{"tokens"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ci", "stefan", "read", "acme-shop"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("the list shows the secret")
	}
}

// A token never reaches beyond its owner's role.
func TestAdminCreateTokenRespectsTheRole(t *testing.T) {
	cli, st, _ := newAdminCLI(t)
	ctx := context.Background()
	seedUser(t, st, "stefan", "password-1234")
	dana, err := st.Users.Create(ctx, "dana", "hash", "none")
	if err != nil {
		t.Fatal(err)
	}
	notes := &store.Project{Name: "Notes", Slug: "notes", Path: "notes"}
	other := &store.Project{Name: "Other", Slug: "other", Path: "other"}
	for _, p := range []*store.Project{notes, other} {
		if err := st.Projects.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Roles.Set(ctx, dana.ID, notes.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if err := cli.run(ctx, []string{"create-token", "--name", "x", "--user", "dana", "--scope", "admin"}); err == nil || !strings.Contains(err.Error(), "at most operate") {
		t.Fatalf("admin scope for a developer: %v", err)
	}
	if err := cli.run(ctx, []string{"create-token", "--name", "x", "--user", "dana", "--project", "other"}); err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("a project she can't see: %v", err)
	}
	if err := cli.run(ctx, []string{"create-token", "--name", "x", "--user", "dana", "--project", "notes"}); err != nil {
		t.Fatal(err)
	}
	if err := cli.run(ctx, []string{"create-token", "--name", "x"}); err == nil || !strings.Contains(err.Error(), "several accounts") {
		t.Fatalf("no --user with two accounts: %v", err)
	}
	if err := cli.run(ctx, []string{"create-token"}); !errors.Is(err, errAdminUsage) {
		t.Fatalf("no --name: %v", err)
	}
}

func TestAdminRevokeToken(t *testing.T) {
	cli, st, out := newAdminCLI(t)
	ctx := context.Background()
	seedUser(t, st, "stefan", "password-1234")
	for _, name := range []string{"ci", "mcp"} {
		if err := cli.run(ctx, []string{"create-token", "--name", name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.run(ctx, []string{"revoke-tokens", "ci"}); err == nil || !strings.Contains(err.Error(), "revoke-token <token>") {
		t.Fatalf("revoke-tokens with an argument: %v", err)
	}
	if err := cli.run(ctx, []string{"revoke-token", "ci"}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.Tokens.List(ctx)
	if len(list) != 1 || list[0].Name != "mcp" {
		t.Fatalf("left: %+v", list)
	}
	// By its prefix, with --user after the token.
	out.Reset()
	if err := cli.run(ctx, []string{"revoke-token", list[0].Prefix, "--user", "stefan"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Tokens.List(ctx); len(list) != 0 {
		t.Fatalf("left: %+v", list)
	}
	if err := cli.run(ctx, []string{"revoke-token", "ci"}); err == nil || !strings.Contains(err.Error(), "no API token") {
		t.Fatalf("unknown token: %v", err)
	}
}

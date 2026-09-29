package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/store"
)

func TestSecretsAtRest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	raw := func(query string, args ...any) string {
		t.Helper()
		var v string
		if err := st.DB().QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return v
	}

	// Values written before encryption (no ring) are plain.
	p := &store.Project{Name: "Vault", Slug: "vault", Path: "vault", Git: store.GitConfig{URL: "https://x/y.git", Token: "ghp_secret"},
		Services: []store.ProjectService{{Kind: store.ServiceDatabase, Variant: "mariadb", Version: "11", Enabled: true, Config: json.RawMessage(`{"password":"dbpass"}`)}},
		Env:      []store.EnvVar{{Key: "API_KEY", Value: "k3y", IsSecret: true}, {Key: "APP_ENV", Value: "local"}}}
	if err := st.Projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Set(ctx, "oidc", `{"clientSecret":"s3"}`); err != nil {
		t.Fatal(err)
	}
	if raw(`SELECT git_token FROM projects`) != "ghp_secret" {
		t.Fatal("plain before encryption")
	}

	defer secrets.SetDefault(nil)
	k1, _ := secrets.GenerateKey()
	ring := secrets.NewRing(k1)
	secrets.SetDefault(ring)
	if err := st.CheckSecretKey(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.ResealSecrets(ctx)
	if err != nil || n != 4 { // token, API_KEY, service config, oidc (the check value is sealed already)
		t.Fatalf("reseal: %d %v", n, err)
	}
	for _, q := range []string{`SELECT git_token FROM projects`, `SELECT value FROM project_environment_variables WHERE key = 'API_KEY'`, `SELECT config FROM project_services`, `SELECT value FROM settings WHERE key = 'oidc'`} {
		if v := raw(q); secrets.KeyIDOf(v) != k1.ID() {
			t.Errorf("%s not sealed: %s", q, v)
		}
	}
	if raw(`SELECT value FROM project_environment_variables WHERE key = 'APP_ENV'`) != "local" {
		t.Error("a plain variable is left alone")
	}
	got, err := st.Projects.Get(ctx, p.ID)
	if err != nil || got.Git.Token != "ghp_secret" || !strings.Contains(string(got.Services[0].Config), "dbpass") || got.Env[0].Value != "k3y" {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if v, _ := st.Settings.Get(ctx, "oidc"); v != `{"clientSecret":"s3"}` {
		t.Fatalf("setting: %s", v)
	}
	if n, _ := st.ResealSecrets(ctx); n != 0 {
		t.Fatalf("second reseal changed %d", n)
	}

	// New writes are sealed right away.
	if err := st.Projects.UpdateGit(ctx, p.ID, store.GitConfig{URL: "https://x/y.git", Token: "new"}); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsSealed(raw(`SELECT git_token FROM projects`)) {
		t.Fatal("new token plain")
	}

	// Another key: the check names the key the database needs.
	k2, _ := secrets.GenerateKey()
	secrets.SetDefault(secrets.NewRing(k2))
	if err := st.CheckSecretKey(ctx); err == nil || !strings.Contains(err.Error(), k1.ID()) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := st.Projects.Get(ctx, p.ID); !errors.Is(err, secrets.ErrUnknownKey) {
		t.Fatalf("read with the wrong key: %v", err)
	}
	// With both keys the database moves to the new one.
	secrets.SetDefault(secrets.NewRing(k2, k1))
	if n, err := st.ResealSecrets(ctx); err != nil || n != 5 {
		t.Fatalf("rotate: %d %v", n, err)
	}
	secrets.SetDefault(secrets.NewRing(k2))
	if err := st.CheckSecretKey(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Projects.Get(ctx, p.ID); err != nil || got.Git.Token != "new" {
		t.Fatalf("after rotation: %v", err)
	}
}

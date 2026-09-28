package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/store"
)

// provider is a minimal OpenID Connect provider: discovery, keys and a token endpoint
// that hands out signed ID tokens for the codes a test registers.
type provider struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]map[string]any
	pkceOK bool
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: key, codes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		claims, ok := p.codes[r.PostForm.Get("code")]
		p.pkceOK = r.PostForm.Get("code_verifier") != ""
		p.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
		all := map[string]any{"iss": p.srv.URL, "aud": "envoryx", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
		for k, v := range claims {
			all[k] = v
		}
		idt, _ := jwt.Signed(signer).Claims(all).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idt})
	})
	return p
}

// signIn runs a sign-in whose ID token carries the claims (the nonce is filled in).
func (p *provider) signIn(t *testing.T, s *Service, claims map[string]any) (store.User, error) {
	t.Helper()
	authURL, state, err := s.Start(context.Background(), "http://envoryx.test/api/v1/auth/oidc/callback", "/projects")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("state") != state || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL: %s", authURL)
	}
	claims["nonce"] = u.Query().Get("nonce")
	code := "code-" + state[:8]
	p.mu.Lock()
	p.codes[code] = claims
	p.mu.Unlock()
	user, returnTo, err := s.Finish(context.Background(), state, code)
	if err == nil && returnTo != "/projects" {
		t.Fatalf("return to %q", returnTo)
	}
	return user, err
}

func newService(t *testing.T) (*Service, *store.Store, *auth.Service) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	a := auth.NewService(st, auth.Options{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, log)
	return New(st, a, log), st, a
}

func TestSingleSignOn(t *testing.T) {
	p := newProvider(t)
	s, st, a := newService(t)
	ctx := context.Background()
	if _, err := st.Users.Create(ctx, "root", "x", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(ctx, "http://x/cb", "/"); err != ErrDisabled {
		t.Fatalf("disabled: %v", err)
	}
	cfg := Config{Enabled: true, Issuer: p.srv.URL, ClientID: "envoryx", ClientSecret: "s3cret", AutoCreate: true, AdminGroups: []string{"envoryx-admins"}, DeveloperGroups: []string{"devs"}}
	if _, err := s.SetConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// Without a username claim the e-mail address stands in; a name is cleaned up.
	if got := usernameFrom(Config{}, map[string]any{"email": "kilgore@kilgore.trout", "name": "Kilgore Trout"}); got != "kilgore" {
		t.Fatalf("from the e-mail: %q", got)
	}
	if got := usernameFrom(Config{}, map[string]any{"name": "Kilgore Trout"}); got != "Kilgore.Trout" {
		t.Fatalf("from the name: %q", got)
	}
	if got := usernameFrom(Config{UsernameClaim: "nickname"}, map[string]any{"email": "a@b.c"}); got != "" {
		t.Fatalf("a configured claim has no stand-in: %q", got)
	}

	// A developer is created with the role of their group, PKCE and nonce included.
	u, err := p.signIn(t, s, map[string]any{"sub": "u1", "preferred_username": "dana", "groups": []string{"devs"}})
	if err != nil || u.Username != "dana" || u.Role != "developer" || !p.pkceOK {
		t.Fatalf("new developer: %+v %v (pkce %v)", u, err, p.pkceOK)
	}
	// The role follows the groups at the next sign-in.
	if u, err = p.signIn(t, s, map[string]any{"sub": "u1", "preferred_username": "dana", "groups": []string{"envoryx-admins"}}); err != nil || u.Role != "admin" {
		t.Fatalf("promoted: %+v %v", u, err)
	}
	// No matching group and the default (deny) refuses.
	if _, err := p.signIn(t, s, map[string]any{"sub": "u2", "preferred_username": "eve", "groups": []string{"marketing"}}); err == nil || !strings.Contains(err.Error(), "none of the groups") {
		t.Fatalf("denied: %v", err)
	}
	// A local account of the same name is not taken over …
	if _, err := p.signIn(t, s, map[string]any{"sub": "u3", "preferred_username": "root", "groups": []string{"envoryx-admins"}}); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("takeover: %v", err)
	}
	// … unless an admin sent it an invitation, which links it.
	_, invited, err := a.InviteUser(ctx, "frank", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	u, err = p.signIn(t, s, map[string]any{"sub": "u4", "preferred_username": "frank", "groups": []string{"devs"}})
	if err != nil || u.ID != invited.ID || u.Role != "developer" {
		t.Fatalf("invited: %+v %v", u, err)
	}
	if got, _ := st.Users.ByID(ctx, invited.ID); got.OIDCSubject == "" || got.InviteHash != "" {
		t.Fatalf("link: %+v", got)
	}

	// Without auto-create only invited users get in.
	cfg.AutoCreate = false
	if _, err := s.SetConfig(ctx, Config{Enabled: true, Issuer: p.srv.URL, ClientID: "envoryx", DeveloperGroups: []string{"devs"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Config(ctx); c.ClientSecret != "s3cret" {
		t.Fatal("an empty secret keeps the stored one")
	}
	if _, err := p.signIn(t, s, map[string]any{"sub": "u5", "preferred_username": "gina", "groups": []string{"devs"}}); err == nil || !strings.Contains(err.Error(), "ask an admin") {
		t.Fatalf("no auto-create: %v", err)
	}

	// A token for another client, a replayed state or a wrong nonce do not get in.
	authURL, state, _ := s.Start(ctx, "http://x/cb", "/")
	_ = authURL
	p.mu.Lock()
	p.codes["forged"] = map[string]any{"sub": "u1", "preferred_username": "dana", "nonce": "wrong"}
	p.mu.Unlock()
	if _, _, err := s.Finish(ctx, state, "forged"); err == nil {
		t.Fatal("wrong nonce accepted")
	}
	if _, _, err := s.Finish(ctx, state, "forged"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replayed state: %v", err)
	}
}

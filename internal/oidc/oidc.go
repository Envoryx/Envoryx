// Package oidc signs users in through an OpenID Connect provider (Authentik, Keycloak,
// Authelia, Google …): the authorization code flow with PKCE, state and nonce, the ID
// token verified against the provider's keys. Groups from the token can decide the
// user's role; the users themselves live in Envoryx's store like local ones.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// SettingKey is where the configuration is stored.
const SettingKey = "oidc"

// RoleDeny as default role refuses users who match no group mapping (or, without a
// mapping, anyone not invited).
const RoleDeny = "deny"

// Config is the provider configuration an admin sets.
type Config struct {
	Enabled bool `json:"enabled"`
	// Name labels the login button ("Sign in with Authentik").
	Name         string `json:"name"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// Scopes asked for besides openid; empty means profile, email and groups.
	Scopes []string `json:"scopes,omitempty"`
	// UsernameClaim names the Envoryx username (default preferred_username), GroupsClaim
	// the list of groups (default groups).
	UsernameClaim string `json:"usernameClaim,omitempty"`
	GroupsClaim   string `json:"groupsClaim,omitempty"`
	// Members of these groups get the role at every sign-in; the first match from admin
	// down wins. Without any group the role is left to Envoryx.
	AdminGroups     []string `json:"adminGroups,omitempty"`
	DeveloperGroups []string `json:"developerGroups,omitempty"`
	ViewerGroups    []string `json:"viewerGroups,omitempty"`
	// DefaultRole is the role of a user no group matches: developer, viewer, none, or
	// deny (the default) to refuse them.
	DefaultRole string `json:"defaultRole,omitempty"`
	// AutoCreate creates the Envoryx user at the first sign-in; without it only users an
	// admin invited can sign in.
	AutoCreate bool `json:"autoCreate"`
}

func (c Config) mapsGroups() bool {
	return len(c.AdminGroups)+len(c.DeveloperGroups)+len(c.ViewerGroups) > 0
}

func (c Config) usernameClaim() string {
	if c.UsernameClaim == "" {
		return "preferred_username"
	}
	return c.UsernameClaim
}

func (c Config) groupsClaim() string {
	if c.GroupsClaim == "" {
		return "groups"
	}
	return c.GroupsClaim
}

func (c Config) scopes() []string {
	s := c.Scopes
	if len(s) == 0 {
		s = []string{"profile", "email", "groups"}
	}
	return append([]string{gooidc.ScopeOpenID}, slices.DeleteFunc(slices.Clone(s), func(v string) bool { return v == gooidc.ScopeOpenID })...)
}

// normalize validates the configuration, trims lists and fills defaults.
func (c Config) normalize() (Config, error) {
	c.Name, c.Issuer, c.ClientID = strings.TrimSpace(c.Name), strings.TrimRight(strings.TrimSpace(c.Issuer), "/"), strings.TrimSpace(c.ClientID)
	clean := func(in []string) []string {
		var out []string
		for _, v := range in {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	c.Scopes, c.AdminGroups, c.DeveloperGroups, c.ViewerGroups = clean(c.Scopes), clean(c.AdminGroups), clean(c.DeveloperGroups), clean(c.ViewerGroups)
	if c.DefaultRole == "" {
		c.DefaultRole = RoleDeny
	}
	switch c.DefaultRole {
	case RoleDeny, string(auth.RoleDeveloper), string(auth.RoleViewer), string(auth.RoleNone):
	default:
		return c, fmt.Errorf("%w: the default role must be developer, viewer, none or deny", validate.ErrInvalid)
	}
	if c.Name == "" {
		c.Name = "SSO"
	}
	if !c.Enabled {
		return c, nil
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return c, fmt.Errorf("%w: the issuer must be an http(s) URL", validate.ErrInvalid)
	}
	if c.ClientID == "" {
		return c, fmt.Errorf("%w: the client ID is required", validate.ErrInvalid)
	}
	return c, nil
}

// pending is a sign-in between the redirect to the provider and its return.
type pending struct {
	verifier, nonce, redirect, returnTo string
	// invite is the invitation token when the sign-in started from an invitation link.
	invite  string
	expires time.Time
}

// Service runs the sign-in.
type Service struct {
	store *store.Store
	auth  *auth.Service
	log   *slog.Logger
	now   func() time.Time

	mu       sync.Mutex
	pending  map[string]pending
	provider *gooidc.Provider
	issuer   string
}

// New creates the service.
func New(st *store.Store, a *auth.Service, log *slog.Logger) *Service {
	return &Service{store: st, auth: a, log: log, now: time.Now, pending: map[string]pending{}}
}

// Config returns the stored configuration (with the client secret).
func (s *Service) Config(ctx context.Context) (Config, error) {
	raw, err := s.store.Settings.Get(ctx, SettingKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Config{}, err
	}
	var c Config
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Config{}, fmt.Errorf("read the OIDC settings: %w", err)
		}
	}
	return c.normalize()
}

// SetConfig stores a configuration. An empty client secret keeps the stored one unless
// clearSecret asks to drop it (e.g. when single sign-on is switched off for good).
func (s *Service) SetConfig(ctx context.Context, c Config, clearSecret bool) (Config, error) {
	c, err := c.normalize()
	if err != nil {
		return Config{}, err
	}
	if c.ClientSecret == "" && !clearSecret {
		if old, err := s.Config(ctx); err == nil {
			c.ClientSecret = old.ClientSecret
		}
	}
	raw, _ := json.Marshal(c)
	if err := s.store.Settings.Set(ctx, SettingKey, string(raw)); err != nil {
		return Config{}, err
	}
	s.mu.Lock()
	s.provider, s.issuer = nil, ""
	s.mu.Unlock()
	return c, nil
}

// providerFor discovers the provider's endpoints and keys, once per issuer.
func (s *Service) providerFor(ctx context.Context, issuer string) (*gooidc.Provider, error) {
	s.mu.Lock()
	if s.provider != nil && s.issuer == issuer {
		p := s.provider
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := gooidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("reach the OpenID Connect provider %s: %w", issuer, err)
	}
	s.mu.Lock()
	s.provider, s.issuer = p, issuer
	s.mu.Unlock()
	return p, nil
}

// Test checks that the issuer answers with a discovery document.
func (s *Service) Test(ctx context.Context, c Config) error {
	c, err := c.normalize()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = gooidc.NewProvider(ctx, c.Issuer)
	return err
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ErrDisabled is returned when single sign-on is not configured.
var ErrDisabled = errors.New("single sign-on is not set up")

// Start begins a sign-in: it returns the provider's authorization URL and the state,
// which the caller binds to the browser (a cookie). redirect is Envoryx's callback URL as
// the browser reaches it; returnTo is where the UI continues afterwards. invite is the
// token of the invitation link the sign-in started from, or "": only that link connects
// an existing Envoryx account to the provider's account.
func (s *Service) Start(ctx context.Context, redirect, returnTo, invite string) (authURL, state string, err error) {
	c, err := s.Config(ctx)
	if err != nil {
		return "", "", err
	}
	if !c.Enabled {
		return "", "", ErrDisabled
	}
	if invite != "" {
		if _, err := s.auth.Invitation(ctx, invite); err != nil {
			return "", "", err
		}
	}
	p, err := s.providerFor(ctx, c.Issuer)
	if err != nil {
		return "", "", err
	}
	state, err = randomString()
	if err != nil {
		return "", "", err
	}
	nonce, err := randomString()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	s.mu.Lock()
	now := s.now()
	for k, v := range s.pending {
		if now.After(v.expires) {
			delete(s.pending, k)
		}
	}
	if len(s.pending) > 1000 {
		s.mu.Unlock()
		return "", "", errors.New("too many sign-ins in progress, try again in a few minutes")
	}
	s.pending[state] = pending{verifier: verifier, nonce: nonce, redirect: redirect, returnTo: returnTo, invite: invite, expires: now.Add(10 * time.Minute)}
	s.mu.Unlock()
	oc := s.oauth(c, p, redirect)
	return oc.AuthCodeURL(state, gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

func (s *Service) oauth(c Config, p *gooidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: redirect, Scopes: c.scopes()}
}

// Finish completes a sign-in with the code the provider returned and resolves the
// Envoryx user. It returns where the UI should continue.
func (s *Service) Finish(ctx context.Context, state, code string) (store.User, string, error) {
	s.mu.Lock()
	pend, ok := s.pending[state]
	delete(s.pending, state)
	s.mu.Unlock()
	if !ok || s.now().After(pend.expires) {
		return store.User{}, "", errors.New("the sign-in expired or was already used; start it again")
	}
	c, err := s.Config(ctx)
	if err != nil {
		return store.User{}, "", err
	}
	if !c.Enabled {
		return store.User{}, "", ErrDisabled
	}
	p, err := s.providerFor(ctx, c.Issuer)
	if err != nil {
		return store.User{}, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := s.oauth(c, p, pend.redirect).Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		return store.User{}, "", fmt.Errorf("the provider refused the sign-in: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return store.User{}, "", errors.New("the provider returned no ID token")
	}
	idt, err := p.Verifier(&gooidc.Config{ClientID: c.ClientID}).Verify(ctx, rawID)
	if err != nil {
		return store.User{}, "", fmt.Errorf("the ID token is not valid: %w", err)
	}
	if idt.Nonce != pend.nonce {
		return store.User{}, "", errors.New("the ID token does not belong to this sign-in")
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return store.User{}, "", err
	}
	// Some providers only put the groups (or the username) into the userinfo answer.
	if _, ok := claims[c.groupsClaim()]; !ok || claims[c.usernameClaim()] == nil {
		if info, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
			extra := map[string]any{}
			if info.Claims(&extra) == nil {
				for k, v := range extra {
					if _, has := claims[k]; !has {
						claims[k] = v
					}
				}
			}
		}
	}
	u, err := s.resolveUser(ctx, c, idt.Issuer+"|"+idt.Subject, claims, pend.invite)
	if err != nil {
		return store.User{}, "", err
	}
	return u, pend.returnTo, nil
}

// roleFor picks the role from the groups: admin, developer and viewer groups in that
// order, else the default role.
func roleFor(c Config, groups []string) string {
	has := func(want []string) bool {
		return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(want, g) })
	}
	switch {
	case has(c.AdminGroups):
		return string(auth.RoleAdmin)
	case has(c.DeveloperGroups):
		return string(auth.RoleDeveloper)
	case has(c.ViewerGroups):
		return string(auth.RoleViewer)
	}
	return c.DefaultRole
}

// usernameFrom picks the Envoryx username from the claims: the configured claim, and
// when the provider sends none (Google, Dex), the part of the e-mail address before the
// @ or the name. Characters a username may not have become dots.
func usernameFrom(c Config, claims map[string]any) string {
	str := func(k string) string { v, _ := claims[k].(string); return strings.TrimSpace(v) }
	name := str(c.usernameClaim())
	if name == "" && c.UsernameClaim == "" {
		if email := str("email"); email != "" {
			name, _, _ = strings.Cut(email, "@")
		} else {
			name = str("name")
		}
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('.')
		}
	}
	return strings.Trim(b.String(), ".")
}

// stringList reads a claim that is a list of strings or one string.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// resolveUser finds the Envoryx user of an account: by its subject, else the user whose
// invitation link the sign-in started from (the admin's go-ahead to link), else a new
// user when AutoCreate is on. A name never links an account: the provider's username can
// be anybody's choice (the part of any e-mail address before the @, a self-chosen
// nickname), and an invitation is also open while a password is reset. With groups
// mapped, the role follows them at every sign-in.
func (s *Service) resolveUser(ctx context.Context, c Config, subject string, claims map[string]any, invite string) (store.User, error) {
	username := usernameFrom(c, claims)
	role := roleFor(c, stringList(claims[c.groupsClaim()]))
	if !c.mapsGroups() {
		role = ""
	}
	u, err := s.store.Users.ByOIDCSubject(ctx, subject)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound) && invite != "":
		local, err := s.auth.Invitation(ctx, invite)
		if err != nil {
			return store.User{}, err
		}
		if local.OIDCSubject != "" {
			return store.User{}, fmt.Errorf("the Envoryx account %s is connected to another single sign-on account already", local.Username)
		}
		if err := s.store.Users.SetOIDCSubject(ctx, local.ID, subject); err != nil {
			return store.User{}, err
		}
		_ = s.store.Users.SetInvite(ctx, local.ID, "", time.Time{})
		// Like a password set through the link: whoever was signed in before is out.
		if err := s.store.Sessions.DeleteByUser(ctx, local.ID); err != nil {
			return store.User{}, err
		}
		local.OIDCSubject, local.InviteHash = subject, ""
		u = local
	case errors.Is(err, store.ErrNotFound):
		if err := validate.Username(username); err != nil {
			return store.User{}, fmt.Errorf("the provider's %s claim %q is no usable username: %w", c.usernameClaim(), username, err)
		}
		_, lerr := s.store.Users.ByUsername(ctx, username)
		switch {
		case lerr == nil:
			return store.User{}, fmt.Errorf("an Envoryx account named %s exists already; to connect it to single sign-on, open its invitation link from an admin and sign in from there", username)
		case !errors.Is(lerr, store.ErrNotFound):
			return store.User{}, lerr
		case !c.AutoCreate:
			return store.User{}, fmt.Errorf("there is no Envoryx account for %s; ask an admin for an invitation", username)
		default:
			newRole := role
			if newRole == "" {
				newRole = c.DefaultRole
			}
			if newRole == RoleDeny {
				return store.User{}, fmt.Errorf("%s is in none of the groups that may use Envoryx", username)
			}
			created, err := s.store.Users.Insert(ctx, store.User{Username: username, Role: newRole, OIDCSubject: subject})
			if err != nil {
				return store.User{}, err
			}
			s.log.Info("single sign-on created a user", "username", username, "role", newRole)
			return created, nil
		}
	default:
		return store.User{}, err
	}
	if u.Disabled {
		return store.User{}, auth.ErrInvalidCredentials
	}
	if role == RoleDeny {
		return store.User{}, fmt.Errorf("%s is in none of the groups that may use Envoryx", u.Username)
	}
	if role != "" && role != u.Role {
		updated, err := s.auth.SetUserRole(ctx, u.ID, auth.Role(role))
		if errors.Is(err, auth.ErrLastAdmin) {
			s.log.Warn("single sign-on would take the last admin's role; kept it", "username", u.Username, "role", role)
			return u, nil
		}
		if err != nil {
			return store.User{}, err
		}
		u = updated
	}
	return u, nil
}

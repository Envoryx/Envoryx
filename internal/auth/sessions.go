package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// CookieName is the session cookie.
const CookieName = "envoryx_session"

// ErrInvalidCredentials is returned for unknown users or wrong passwords.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrTooManyAttempts is returned when login rate limiting kicks in.
var ErrTooManyAttempts = errors.New("too many failed login attempts, try again later")

// ErrUnauthenticated is returned when no valid session is present.
var ErrUnauthenticated = errors.New("unauthenticated")

// Principal is the authenticated user attached to a request context.
type Principal struct {
	UserID    string
	Username  string
	Role      string
	SessionID string
	// TokenName is set when the request was authenticated with an API token (REST
	// bearer, MCP or SSH) instead of a browser session.
	TokenName string
	// Scope and Projects are the token's access level and project restriction; empty
	// Projects means all projects. Both mean nothing for sessions.
	Scope    Scope
	Projects []string
	// ProjectRoles are the user's roles in particular projects, which replace the
	// global Role there.
	ProjectRoles map[string]Role
}

// Options configure the session service.
type Options struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	SecureCookies   bool
}

// Service handles login, logout and session validation.
type Service struct {
	store *store.Store
	opts  Options
	log   *slog.Logger
	now   func() time.Time

	limiter *loginLimiter
	// plan is the hoster's plan of a managed instance; it caps the active users.
	plan *plan.Holder
}

// SetPlan hands the service the instance's plan.
func (s *Service) SetPlan(h *plan.Holder) { s.plan = h }

// CheckUserQuota refuses one more active user when the plan's users are taken.
func (s *Service) CheckUserQuota(ctx context.Context) error {
	pl := s.plan.Get()
	if pl == nil || pl.Limits.Users == 0 {
		return nil
	}
	max := pl.Limits.Users
	n, err := s.store.Users.CountActive(ctx)
	if err != nil {
		return err
	}
	if n >= max {
		return plan.Quota("this instance has reached the plan's user limit (%d); disable a user first", max)
	}
	return nil
}

// NewService creates a session service.
func NewService(st *store.Store, opts Options, log *slog.Logger) *Service {
	return &Service{
		store:   st,
		opts:    opts,
		log:     log,
		now:     func() time.Time { return time.Now().UTC() },
		limiter: newLoginLimiter(),
	}
}

// NeedsSetup reports whether no user exists yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.store.Users.Count(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// CreateInitialAdmin creates the first user. It fails if a user already exists.
func (s *Service) CreateInitialAdmin(ctx context.Context, username, password string) (store.User, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return store.User{}, err
	}
	if !needs {
		return store.User{}, fmt.Errorf("setup already completed: %w", store.ErrConflict)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	return s.store.Users.Create(ctx, username, hash, "admin")
}

// Login validates credentials and creates a session. The returned token is the raw
// cookie value; only its hash is persisted.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (token string, user store.User, err error) {
	if !s.limiter.allow(ip, username, s.now()) {
		return "", store.User{}, ErrTooManyAttempts
	}
	user, err = s.store.Users.ByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Burn comparable time so user enumeration via timing is harder.
			_, _ = VerifyPassword(dummyHash, password)
			s.limiter.fail(ip, username, s.now())
			return "", store.User{}, ErrInvalidCredentials
		}
		return "", store.User{}, err
	}
	// An invited user has no password yet, one who signs in through OpenID Connect may
	// have none at all; a disabled one may not sign in. All look like a wrong password.
	if user.PasswordHash == "" || user.Disabled {
		_, _ = VerifyPassword(dummyHash, password)
		s.limiter.fail(ip, username, s.now())
		return "", store.User{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		return "", store.User{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		s.limiter.fail(ip, username, s.now())
		return "", store.User{}, ErrInvalidCredentials
	}
	s.limiter.reset(ip, username)
	token, err = s.StartSession(ctx, user, ip, userAgent)
	if err != nil {
		return "", store.User{}, err
	}
	return token, user, nil
}

// StartSession opens a session for a user who has proven who they are (password, OpenID
// Connect) and returns its raw token.
func (s *Service) StartSession(ctx context.Context, user store.User, ip, userAgent string) (token string, err error) {
	if user.Disabled {
		return "", ErrInvalidCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	ts := s.now()
	sess := store.Session{
		ID:         hashToken(token),
		UserID:     user.ID,
		CreatedAt:  ts,
		ExpiresAt:  ts.Add(s.opts.IdleTimeout),
		LastSeenAt: ts,
		IP:         ip,
		UserAgent:  truncate(userAgent, 256),
	}
	if err := s.store.Sessions.Create(ctx, sess); err != nil {
		return "", err
	}
	return token, nil
}

// PrincipalFor is the principal of a user signing in some other way (an SSH key).
func (s *Service) PrincipalFor(ctx context.Context, user store.User) (Principal, error) {
	if user.Disabled {
		return Principal{}, ErrUnauthenticated
	}
	return s.principal(ctx, user)
}

// principal builds the principal of a user with the user's project roles.
func (s *Service) principal(ctx context.Context, user store.User) (Principal, error) {
	roles, err := s.store.Roles.ByUser(ctx, user.ID)
	if err != nil {
		return Principal{}, err
	}
	p := Principal{UserID: user.ID, Username: user.Username, Role: user.Role}
	if len(roles) > 0 {
		p.ProjectRoles = make(map[string]Role, len(roles))
		for id, r := range roles {
			p.ProjectRoles[id] = Role(r)
		}
	}
	return p, nil
}

// Validate resolves a raw token into a principal, sliding the idle expiry forward.
func (s *Service) Validate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	id := hashToken(token)
	sess, err := s.store.Sessions.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	ts := s.now()
	if ts.After(sess.ExpiresAt) || ts.After(sess.CreatedAt.Add(s.opts.AbsoluteTimeout)) {
		_ = s.store.Sessions.Delete(ctx, id)
		return Principal{}, ErrUnauthenticated
	}
	user, err := s.store.Users.ByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = s.store.Sessions.Delete(ctx, id)
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	if user.Disabled {
		_ = s.store.Sessions.Delete(ctx, id)
		return Principal{}, ErrUnauthenticated
	}
	// Only touch the row every minute to avoid a write per request.
	if ts.Sub(sess.LastSeenAt) > time.Minute {
		if err := s.store.Sessions.Touch(ctx, id, ts, ts.Add(s.opts.IdleTimeout)); err != nil {
			s.log.Warn("touch session failed", "err", err)
		}
	}
	p, err := s.principal(ctx, user)
	if err != nil {
		return Principal{}, err
	}
	p.SessionID = id
	return p, nil
}

// Logout deletes the session behind a raw token.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.Sessions.Delete(ctx, hashToken(token))
}

// ChangePassword verifies the current password, stores the new one and revokes all other sessions.
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	user, err := s.store.Users.ByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(user.PasswordHash, current)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.store.Users.UpdatePassword(ctx, user.ID, hash); err != nil {
		return err
	}
	return s.store.Sessions.DeleteByUser(ctx, user.ID)
}

// Cookie builds the session cookie for a raw token. An empty token clears the cookie.
func (s *Service) Cookie(token string) *http.Cookie {
	c := &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}
	if token == "" {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(s.opts.AbsoluteTimeout.Seconds())
	}
	return c
}

// PurgeExpired deletes expired sessions.
func (s *Service) PurgeExpired(ctx context.Context) {
	if n, err := s.store.Sessions.DeleteExpired(ctx, s.now()); err != nil {
		s.log.Warn("purge sessions failed", "err", err)
	} else if n > 0 {
		s.log.Debug("purged expired sessions", "count", n)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// dummyHash is used to equalise timing for unknown usernames.
var dummyHash = func() string {
	h, err := HashPassword("envoryx-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()

// loginLimiter is a small in-memory brute-force limiter keyed by IP and username.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	blocked  time.Time
	last     time.Time
}

const (
	limiterMaxFailures = 5
	limiterWindow      = 15 * time.Minute
	limiterBaseBlock   = 30 * time.Second
	limiterMaxBlock    = 15 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*limitEntry{}}
}

func (l *loginLimiter) keys(ip, user string) []string {
	return []string{"ip:" + ip, "user:" + user}
}

func (l *loginLimiter) allow(ip, user string, ts time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(ts)
	for _, k := range l.keys(ip, user) {
		if e, ok := l.entries[k]; ok && ts.Before(e.blocked) {
			return false
		}
	}
	return true
}

func (l *loginLimiter) fail(ip, user string, ts time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.keys(ip, user) {
		e, ok := l.entries[k]
		if !ok || ts.Sub(e.last) > limiterWindow {
			e = &limitEntry{}
			l.entries[k] = e
		}
		e.failures++
		e.last = ts
		if e.failures >= limiterMaxFailures {
			block := limiterBaseBlock << uint(min(e.failures-limiterMaxFailures, 5))
			if block > limiterMaxBlock {
				block = limiterMaxBlock
			}
			e.blocked = ts.Add(block)
		}
	}
}

func (l *loginLimiter) reset(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.keys(ip, user) {
		delete(l.entries, k)
	}
}

func (l *loginLimiter) gc(ts time.Time) {
	if len(l.entries) < 1000 {
		return
	}
	for k, e := range l.entries {
		if ts.Sub(e.last) > limiterWindow && ts.After(e.blocked) {
			delete(l.entries, k)
		}
	}
}

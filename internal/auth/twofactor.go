package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is HMAC-SHA1; authenticator apps expect it
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Two-factor authentication: a user who signs in with a password proves a second factor
// when they have set one up, a TOTP code from an authenticator app or a passkey
// (passkey.go), or uses one of their recovery codes. A passkey also signs in on its own,
// since it checks possession and the PIN or biometrics itself. The password step hands
// out a ticket instead of a session; the second step redeems it.
//
// The admins can require a second factor of the admins or of everyone
// (SettingTwoFactorRequired). It applies to sign-ins with a password: single sign-on
// leaves it to the identity provider, and the hoster's support signs in with the admins'
// consent. A session the requirement applies to whose user has no second factor yet can
// only set one up (Principal.TwoFactorSetup).

// How a session was started (store.Session.Method).
const (
	MethodPassword = "password" // a password, the user had no second factor
	MethodTOTP     = "totp"     // a password and a TOTP code
	MethodRecovery = "recovery" // a password and a recovery code
	MethodPasskey  = "passkey"  // a passkey, alone or after the password
	MethodInvite   = "invite"   // an invitation or password reset link
	MethodOIDC     = "oidc"     // single sign-on
	MethodSupport  = "support"  // the hoster's support login link
)

// SettingTwoFactorRequired is whom the instance requires a second factor of.
const SettingTwoFactorRequired = "two_factor_required"

// Requirement is whom the instance requires a second factor of.
type Requirement string

const (
	RequireNobody Requirement = "off"
	RequireAdmins Requirement = "admins"
	RequireAll    Requirement = "all"
)

// ParseRequirement checks a requirement from a request.
func ParseRequirement(s string) (Requirement, error) {
	switch r := Requirement(s); r {
	case RequireNobody, RequireAdmins, RequireAll:
		return r, nil
	}
	return "", fmt.Errorf("%w: the two-factor requirement is off, admins or all", validate.ErrInvalid)
}

var (
	// ErrSecondFactor is returned by Login for a user who has to prove a second factor.
	ErrSecondFactor = errors.New("this account uses two-factor authentication")
	// ErrInvalidCode is returned for a wrong TOTP or recovery code.
	ErrInvalidCode = errors.New("the code is wrong or was used already")
	// ErrTicket is returned for an unknown or expired second-factor ticket.
	ErrTicket = errors.New("the sign-in took too long or failed too often; sign in again")
	// ErrNoPassword is returned when a user without a password sets up a second factor.
	ErrNoPassword = fmt.Errorf("%w: two-factor authentication protects the sign-in with a password, and this account signs in through single sign-on; set up a second factor with the identity provider", validate.ErrInvalid)
	// ErrFactorRequired is returned when the last second factor of a user the requirement
	// applies to would go.
	ErrFactorRequired = fmt.Errorf("%w: this instance requires two-factor authentication of this account; set up another factor before removing the last one", validate.ErrInvalid)
	// ErrNoFactor is returned for recovery codes of a user without a second factor.
	ErrNoFactor = fmt.Errorf("%w: set up a second factor first", validate.ErrInvalid)
)

const (
	totpDigits = 6
	totpPeriod = 30
	// totpSkew is how many time steps a code may be off either way (clock drift).
	totpSkew = 1
	// totpSetupTTL is how long a TOTP secret waits to be confirmed.
	totpSetupTTL = 10 * time.Minute

	ticketTTL = 5 * time.Minute
	// ticketTries is how many wrong codes a ticket takes before it ends.
	ticketTries = 5
	// maxPending caps the open tickets, TOTP setups and passkey ceremonies each: the
	// password step and the passkey sign-in are open to anyone.
	maxPending = 1000

	recoveryCodeCount = 10
)

// mfaState holds what waits for a second step: tickets after the password, TOTP secrets
// before their first code.
type mfaState struct {
	mu        sync.Mutex
	tickets   map[string]*mfaTicket
	totpSetup map[string]totpSetup
}

type mfaTicket struct {
	userID  string
	expires time.Time
	tries   int
}

type totpSetup struct {
	secret  string
	expires time.Time
}

func (s *Service) mfa() *mfaState {
	s.mfaOnce.Do(func() {
		s.mfaPending = &mfaState{tickets: map[string]*mfaTicket{}, totpSetup: map[string]totpSetup{}}
	})
	return s.mfaPending
}

// Requirement returns whom the instance requires a second factor of.
func (s *Service) Requirement(ctx context.Context) (Requirement, error) {
	v, err := s.store.Settings.Get(ctx, SettingTwoFactorRequired)
	if errors.Is(err, store.ErrNotFound) || v == "" {
		return RequireNobody, nil
	}
	if err != nil {
		return "", err
	}
	if r, err := ParseRequirement(v); err == nil {
		return r, nil
	}
	return RequireNobody, nil
}

// SetRequirement sets whom the instance requires a second factor of.
func (s *Service) SetRequirement(ctx context.Context, r Requirement) error {
	if _, err := ParseRequirement(string(r)); err != nil {
		return err
	}
	return s.store.Settings.Set(ctx, SettingTwoFactorRequired, string(r))
}

// requiredOf reports whether the requirement covers the user (whatever the sign-in).
func requiredOf(req Requirement, user store.User) bool {
	switch req {
	case RequireAll:
		return true
	case RequireAdmins:
		return Role(user.Role) == RoleAdmin || user.Role == ""
	}
	return false
}

// coveredMethod reports whether the requirement applies to a session started so: not to
// single sign-on and the support, nor to a session from before methods were recorded of
// a user without a password (one who signs in through single sign-on).
func coveredMethod(method string, user store.User) bool {
	switch method {
	case MethodOIDC, MethodSupport:
		return false
	case "":
		return user.PasswordHash != "" && !user.Support
	}
	return true
}

// needsSetup reports whether a session of user, started by method, may only set up a
// second factor: the requirement covers it and the user has none.
func (s *Service) needsSetup(ctx context.Context, user store.User, method string) (bool, error) {
	if !coveredMethod(method, user) {
		return false, nil
	}
	req, err := s.Requirement(ctx)
	if err != nil || !requiredOf(req, user) {
		return false, err
	}
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil {
		return false, err
	}
	return !f.Any(), nil
}

// SecondStep is a password sign-in that waits for a second factor.
type SecondStep struct {
	Ticket string `json:"ticket"`
	// Methods are what the user can prove it with: totp, passkey, recovery.
	Methods []string `json:"methods"`
}

// PasswordLogin checks the password. A user without a second factor gets a session
// (token); one with a factor gets a SecondStep to prove it with.
func (s *Service) PasswordLogin(ctx context.Context, username, password, ip, userAgent string) (token string, user store.User, step *SecondStep, err error) {
	user, err = s.checkPassword(ctx, username, password, ip)
	if err != nil {
		return "", store.User{}, nil, err
	}
	step, err = s.secondStep(ctx, user)
	if err != nil || step != nil {
		return "", user, step, err
	}
	token, err = s.StartSession(ctx, user, MethodPassword, ip, userAgent)
	return token, user, nil, err
}

// SecondStepFor returns the second step a user who proved who they are by other means
// (an invitation link) still has to take; nil when they have no second factor.
func (s *Service) SecondStepFor(ctx context.Context, user store.User) (*SecondStep, error) {
	return s.secondStep(ctx, user)
}

func (s *Service) secondStep(ctx context.Context, user store.User) (*SecondStep, error) {
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil || !f.Any() {
		return nil, err
	}
	ticket, err := s.issueTicket(user.ID)
	if err != nil {
		return nil, err
	}
	var methods []string
	if f.TOTP {
		methods = append(methods, "totp")
	}
	if f.Passkeys > 0 {
		methods = append(methods, "passkey")
	}
	if f.RecoveryCodes > 0 {
		methods = append(methods, "recovery")
	}
	return &SecondStep{Ticket: ticket, Methods: methods}, nil
}

func (s *Service) issueTicket(userID string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	m := s.mfa()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := s.now()
	for k, t := range m.tickets {
		if now.After(t.expires) {
			delete(m.tickets, k)
		}
	}
	if len(m.tickets) >= maxPending {
		return "", ErrTooManyAttempts
	}
	m.tickets[hashToken(token)] = &mfaTicket{userID: userID, expires: now.Add(ticketTTL)}
	return token, nil
}

// ticketUser returns the user a ticket was issued to, without using it up.
func (s *Service) ticketUser(token string) (string, error) {
	m := s.mfa()
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tickets[hashToken(token)]
	if token == "" || t == nil || s.now().After(t.expires) {
		return "", ErrTicket
	}
	return t.userID, nil
}

// TicketUsername names the user of a second-factor ticket, for the audit log; "" for
// an unknown ticket.
func (s *Service) TicketUsername(ctx context.Context, ticket string) string {
	id, err := s.ticketUser(ticket)
	if err != nil {
		return ""
	}
	u, err := s.store.Users.ByID(ctx, id)
	if err != nil {
		return ""
	}
	return u.Username
}

// ticketFailed counts a wrong code against the ticket; it ends after ticketTries.
func (s *Service) ticketFailed(token string) {
	m := s.mfa()
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.tickets[hashToken(token)]; t != nil {
		if t.tries++; t.tries >= ticketTries {
			delete(m.tickets, hashToken(token))
		}
	}
}

func (s *Service) endTicket(token string) {
	m := s.mfa()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tickets, hashToken(token))
}

// CompleteTOTP finishes a password sign-in with a TOTP code.
func (s *Service) CompleteTOTP(ctx context.Context, ticket, code, ip, userAgent string) (string, store.User, error) {
	return s.completeWithCode(ctx, ticket, ip, userAgent, MethodTOTP, func(user store.User) (bool, error) {
		return s.takeTOTP(ctx, user.ID, code)
	})
}

// CompleteRecovery finishes a password sign-in with a recovery code, which is then used up.
func (s *Service) CompleteRecovery(ctx context.Context, ticket, code, ip, userAgent string) (string, store.User, error) {
	return s.completeWithCode(ctx, ticket, ip, userAgent, MethodRecovery, func(user store.User) (bool, error) {
		return s.store.MFA.UseRecoveryCode(ctx, user.ID, hashRecoveryCode(code))
	})
}

func (s *Service) completeWithCode(ctx context.Context, ticket, ip, userAgent, method string, check func(store.User) (bool, error)) (string, store.User, error) {
	userID, err := s.ticketUser(ticket)
	if err != nil {
		return "", store.User{}, err
	}
	key := "mfa:" + userID
	if !s.limiter.allow(ip, key, s.now()) {
		return "", store.User{}, ErrTooManyAttempts
	}
	user, err := s.store.Users.ByID(ctx, userID)
	if err != nil || user.Disabled {
		s.endTicket(ticket)
		return "", store.User{}, ErrTicket
	}
	ok, err := check(user)
	if err != nil {
		return "", store.User{}, err
	}
	if !ok {
		s.ticketFailed(ticket)
		s.limiter.fail(ip, key, s.now())
		return "", user, ErrInvalidCode
	}
	s.endTicket(ticket)
	s.limiter.reset(ip, key)
	token, err := s.StartSession(ctx, user, method, ip, userAgent)
	return token, user, err
}

// takeTOTP checks a code against the user's secret and uses its time step up.
func (s *Service) takeTOTP(ctx context.Context, userID, code string) (bool, error) {
	secret, last, err := s.store.MFA.TOTP(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	step, ok := totpMatch(secret, code, s.now())
	if !ok || step <= last {
		return false, nil
	}
	return s.store.MFA.UseTOTPStep(ctx, userID, step)
}

// ---- managing the own factors ----------------------------------------------------------

// TwoFactorStatus is what a user has set up, for the account page.
type TwoFactorStatus struct {
	// Available is false for a user without a password (single sign-on only).
	Available     bool          `json:"available"`
	TOTP          bool          `json:"totp"`
	Passkeys      []PasskeyInfo `json:"passkeys"`
	RecoveryCodes int           `json:"recoveryCodes"`
	Required      bool          `json:"required"`
}

// PasskeyInfo is a passkey as the account page shows it.
type PasskeyInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RPID       string    `json:"rpId"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
}

// TwoFactorStatus returns what the principal's user has set up.
func (s *Service) TwoFactorStatus(ctx context.Context, p Principal) (TwoFactorStatus, error) {
	user, err := s.store.Users.ByID(ctx, p.UserID)
	if err != nil {
		return TwoFactorStatus{}, err
	}
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil {
		return TwoFactorStatus{}, err
	}
	keys, err := s.store.MFA.Passkeys(ctx, user.ID)
	if err != nil {
		return TwoFactorStatus{}, err
	}
	req, err := s.Requirement(ctx)
	if err != nil {
		return TwoFactorStatus{}, err
	}
	st := TwoFactorStatus{Available: user.PasswordHash != "", TOTP: f.TOTP, RecoveryCodes: f.RecoveryCodes, Required: requiredOf(req, user), Passkeys: []PasskeyInfo{}}
	for _, k := range keys {
		st.Passkeys = append(st.Passkeys, PasskeyInfo{ID: k.ID, Name: k.Name, RPID: k.RPID, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt})
	}
	return st, nil
}

// reauth checks the principal's password before a change to its factors: a session
// left open must not be enough to add or remove one.
func (s *Service) reauth(ctx context.Context, p Principal, password string) (store.User, error) {
	user, err := s.store.Users.ByID(ctx, p.UserID)
	if err != nil {
		return store.User{}, err
	}
	if user.PasswordHash == "" || user.Support {
		return store.User{}, ErrNoPassword
	}
	key := "reauth:" + user.ID
	if !s.limiter.allow("", key, s.now()) {
		return store.User{}, ErrTooManyAttempts
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		return store.User{}, err
	}
	if !ok {
		s.limiter.fail("", key, s.now())
		return store.User{}, ErrWrongPassword
	}
	s.limiter.reset("", key)
	return user, nil
}

// BeginTOTP starts setting up TOTP: a new secret waits for its first code
// (ConfirmTOTP). It returns the secret (base32) and the otpauth URI for the QR code.
func (s *Service) BeginTOTP(ctx context.Context, p Principal, password string) (secret, uri string, err error) {
	user, err := s.reauth(ctx, p, password)
	if err != nil {
		return "", "", err
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	m := s.mfa()
	m.mu.Lock()
	now := s.now()
	for k, v := range m.totpSetup {
		if now.After(v.expires) {
			delete(m.totpSetup, k)
		}
	}
	if len(m.totpSetup) >= maxPending {
		m.mu.Unlock()
		return "", "", ErrTooManyAttempts
	}
	m.totpSetup[user.ID] = totpSetup{secret: secret, expires: now.Add(totpSetupTTL)}
	m.mu.Unlock()
	label := url.PathEscape("Envoryx:" + user.Username)
	q := url.Values{"secret": {secret}, "issuer": {"Envoryx"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return secret, "otpauth://totp/" + label + "?" + q.Encode(), nil
}

// ConfirmTOTP stores the secret BeginTOTP made once a code from the app matches. The
// first second factor of a user comes with recovery codes, returned here once.
func (s *Service) ConfirmTOTP(ctx context.Context, p Principal, code string) (recovery []string, err error) {
	m := s.mfa()
	m.mu.Lock()
	setup, ok := m.totpSetup[p.UserID]
	m.mu.Unlock()
	if !ok || s.now().After(setup.expires) {
		return nil, fmt.Errorf("%w: the setup expired; start again", validate.ErrInvalid)
	}
	step, ok := totpMatch(setup.secret, code, s.now())
	if !ok {
		return nil, ErrInvalidCode
	}
	before, err := s.store.MFA.Factors(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.store.MFA.SetTOTP(ctx, p.UserID, setup.secret, step); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.totpSetup, p.UserID)
	m.mu.Unlock()
	return s.firstFactorCodes(ctx, p.UserID, before)
}

// RemoveTOTP removes the principal's TOTP secret.
func (s *Service) RemoveTOTP(ctx context.Context, p Principal, password string) error {
	user, err := s.reauth(ctx, p, password)
	if err != nil {
		return err
	}
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil {
		return err
	}
	if !f.TOTP {
		return store.ErrNotFound
	}
	if err := s.keepsAFactor(ctx, user, f.Passkeys > 0); err != nil {
		return err
	}
	if err := s.store.MFA.DeleteTOTP(ctx, user.ID); err != nil {
		return err
	}
	return s.dropCodesWithoutFactor(ctx, user.ID)
}

// keepsAFactor refuses to remove the last factor of a user the requirement covers.
func (s *Service) keepsAFactor(ctx context.Context, user store.User, othersLeft bool) error {
	if othersLeft {
		return nil
	}
	req, err := s.Requirement(ctx)
	if err != nil {
		return err
	}
	if requiredOf(req, user) {
		return ErrFactorRequired
	}
	return nil
}

// dropCodesWithoutFactor removes the recovery codes once no factor is left: they would
// otherwise come back to life with the next one.
func (s *Service) dropCodesWithoutFactor(ctx context.Context, userID string) error {
	f, err := s.store.MFA.Factors(ctx, userID)
	if err != nil || f.Any() {
		return err
	}
	return s.store.MFA.SetRecoveryCodes(ctx, userID, nil)
}

// firstFactorCodes gives a user recovery codes when the factor just added is the first.
func (s *Service) firstFactorCodes(ctx context.Context, userID string, before store.Factors) ([]string, error) {
	if before.Any() && before.RecoveryCodes > 0 {
		return nil, nil
	}
	return s.newRecoveryCodes(ctx, userID)
}

// RegenerateRecoveryCodes replaces the principal's recovery codes and returns the new ones.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, p Principal, password string) ([]string, error) {
	user, err := s.reauth(ctx, p, password)
	if err != nil {
		return nil, err
	}
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if !f.Any() {
		return nil, ErrNoFactor
	}
	return s.newRecoveryCodes(ctx, user.ID)
}

func (s *Service) newRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)
	for i := range codes {
		raw := make([]byte, 7)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		c := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))[:10]
		codes[i] = c[:5] + "-" + c[5:]
		hashes[i] = hashRecoveryCode(codes[i])
	}
	if err := s.store.MFA.SetRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// hashRecoveryCode hashes a recovery code as typed: case and separators don't matter.
func hashRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		if (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7') {
			b.WriteRune(r)
		}
	}
	sum := sha256.Sum256([]byte("envoryx-recovery:" + b.String()))
	return hex.EncodeToString(sum[:])
}

// ResetTwoFactor removes every second factor of another user (an admin helping someone
// who lost their device). The admin's own factors go through their account page, with
// their password.
func (s *Service) ResetTwoFactor(ctx context.Context, actor Principal, userID string) (store.User, error) {
	if actor.UserID == userID {
		return store.User{}, fmt.Errorf("%w: remove your own second factors on your account page", validate.ErrInvalid)
	}
	u, err := s.store.Users.ByID(ctx, userID)
	if err != nil {
		return store.User{}, err
	}
	return u, s.store.MFA.Reset(ctx, userID)
}

// ---- TOTP (RFC 6238) ---------------------------------------------------------------------

// totpMatch checks code against secret at t and returns the time step it matched.
func totpMatch(secret, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	now := t.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		if hmac.Equal([]byte(totpCode(key, now+d)), []byte(code)) {
			return now + d, true
		}
	}
	return 0, false
}

func totpCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Passkeys are WebAuthn credentials bound to the host name the UI was opened under (the
// relying party id): a passkey registered at envoryx.example.com works there and
// nowhere else. Browsers only offer WebAuthn in a secure context, so the UI must be
// reached over https with a host name (or at localhost). Registration and sign-in ask the
// authenticator to verify the user (PIN, biometrics), which is why a passkey signs in on
// its own. The passkey is discoverable, so the sign-in page needs no user name.

// ceremonyTTL is how long the browser has to answer a passkey ceremony.
const ceremonyTTL = 5 * time.Minute

// maxPasskeyName caps a passkey's name.
const maxPasskeyName = 64

var (
	// ErrPasskeyContext is returned when the UI isn't opened where passkeys work.
	ErrPasskeyContext = fmt.Errorf("%w: passkeys need the web interface at an https address with a host name (or at localhost)", validate.ErrInvalid)
	// ErrPasskey is returned when a passkey doesn't sign in.
	ErrPasskey = errors.New("the passkey was not accepted")
	// ErrCeremony is returned for an unknown or expired ceremony.
	ErrCeremony = fmt.Errorf("%w: the passkey request expired; try again", validate.ErrInvalid)
)

// RelyingParty is where a passkey ceremony runs: the host name (ID) and the origin the
// browser reports.
type RelyingParty struct {
	ID     string
	Origin string
}

// RelyingPartyFor derives the relying party from the browser's Origin, which the CSRF
// check has matched to the request's host (or to an allowed origin, the dev server).
func RelyingPartyFor(origin string) (RelyingParty, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return RelyingParty{}, ErrPasskeyContext
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil {
		return RelyingParty{}, ErrPasskeyContext
	}
	if u.Scheme != "https" && host != "localhost" {
		return RelyingParty{}, ErrPasskeyContext
	}
	return RelyingParty{ID: host, Origin: u.Scheme + "://" + u.Host}, nil
}

func (rp RelyingParty) webauthn() (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          rp.ID,
		RPDisplayName: "Envoryx",
		RPOrigins:     []string{rp.Origin},
	})
}

// ceremonies are the passkey registrations and sign-ins under way.
type ceremonies struct {
	mu sync.Mutex
	m  map[string]*ceremony
}

type ceremony struct {
	session webauthn.SessionData
	rp      RelyingParty
	// userID is the user registering a passkey, or proving one after the password.
	userID string
	// ticket is the second-factor ticket the sign-in finishes; empty for a sign-in with
	// the passkey alone and for a registration.
	ticket   string
	register bool
	expires  time.Time
}

func (c *ceremonies) put(cer *ceremony, now time.Time) (string, error) {
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*ceremony{}
	}
	for k, v := range c.m {
		if now.After(v.expires) {
			delete(c.m, k)
		}
	}
	if len(c.m) >= maxPending {
		return "", ErrTooManyAttempts
	}
	cer.expires = now.Add(ceremonyTTL)
	c.m[hashToken(id)] = cer
	return id, nil
}

// take returns a ceremony and ends it: each is answered once.
func (c *ceremonies) take(id string, now time.Time) (*ceremony, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cer := c.m[hashToken(id)]
	delete(c.m, hashToken(id))
	if id == "" || cer == nil || now.After(cer.expires) {
		return nil, ErrCeremony
	}
	return cer, nil
}

// waUser is a user with the passkeys of one relying party, as the WebAuthn library wants it.
type waUser struct {
	user  store.User
	creds []webauthn.Credential
	// rows are the stored passkeys behind creds, by credential id.
	rows map[string]store.Passkey
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(u.user.ID) }
func (u *waUser) WebAuthnName() string                       { return u.user.Username }
func (u *waUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// loadWAUser loads a user with the passkeys registered for rp.
func (s *Service) loadWAUser(ctx context.Context, userID string, rp RelyingParty) (*waUser, error) {
	user, err := s.store.Users.ByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.MFA.Passkeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	u := &waUser{user: user, rows: map[string]store.Passkey{}}
	for _, k := range keys {
		if k.RPID != rp.ID {
			continue
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Credential), &c); err != nil {
			continue
		}
		u.creds = append(u.creds, c)
		u.rows[k.ID] = k
	}
	return u, nil
}

// BeginPasskeyRegistration starts adding a passkey for the principal at rp, after
// checking the password. It returns the ceremony id and the options for
// navigator.credentials.create.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, p Principal, password string, rp RelyingParty) (string, *protocol.CredentialCreation, error) {
	if _, err := s.reauth(ctx, p, password); err != nil {
		return "", nil, err
	}
	u, err := s.loadWAUser(ctx, p.UserID, rp)
	if err != nil {
		return "", nil, err
	}
	wa, err := rp.webauthn()
	if err != nil {
		return "", nil, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := wa.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(exclude))
	if err != nil {
		return "", nil, err
	}
	id, err := s.ceremonies.put(&ceremony{session: *session, rp: rp, userID: p.UserID, register: true}, s.now())
	if err != nil {
		return "", nil, err
	}
	return id, creation, nil
}

// FinishPasskeyRegistration stores the passkey the browser created. The first second
// factor of a user comes with recovery codes, returned here once.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, p Principal, id, name string, response []byte) (PasskeyInfo, []string, error) {
	cer, err := s.ceremonies.take(id, s.now())
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	if !cer.register || cer.userID != p.UserID {
		return PasskeyInfo{}, nil, ErrCeremony
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if utf8.RuneCountInString(name) > maxPasskeyName {
		return PasskeyInfo{}, nil, fmt.Errorf("%w: the name is longer than %d characters", validate.ErrInvalid, maxPasskeyName)
	}
	u, err := s.loadWAUser(ctx, p.UserID, cer.rp)
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	wa, err := cer.rp.webauthn()
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return PasskeyInfo{}, nil, fmt.Errorf("%w: %v", ErrPasskey, err)
	}
	cred, err := wa.CreateCredential(u, cer.session, parsed)
	if err != nil {
		return PasskeyInfo{}, nil, fmt.Errorf("%w: %v", ErrPasskey, err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	before, err := s.store.MFA.Factors(ctx, p.UserID)
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	k, err := s.store.MFA.AddPasskey(ctx, store.Passkey{
		ID: base64.RawURLEncoding.EncodeToString(cred.ID), UserID: p.UserID, Name: name, RPID: cer.rp.ID, Credential: string(raw),
	})
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	codes, err := s.firstFactorCodes(ctx, p.UserID, before)
	if err != nil {
		return PasskeyInfo{}, nil, err
	}
	return PasskeyInfo{ID: k.ID, Name: k.Name, RPID: k.RPID, CreatedAt: k.CreatedAt}, codes, nil
}

// RenamePasskey renames one of the principal's passkeys.
func (s *Service) RenamePasskey(ctx context.Context, p Principal, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxPasskeyName {
		return fmt.Errorf("%w: a passkey's name has 1 to %d characters", validate.ErrInvalid, maxPasskeyName)
	}
	return s.store.MFA.RenamePasskey(ctx, p.UserID, id, name)
}

// RemovePasskey removes one of the principal's passkeys, after checking the password.
func (s *Service) RemovePasskey(ctx context.Context, p Principal, id, password string) error {
	user, err := s.reauth(ctx, p, password)
	if err != nil {
		return err
	}
	k, err := s.store.MFA.Passkey(ctx, id)
	if err != nil || k.UserID != user.ID {
		return store.ErrNotFound
	}
	f, err := s.store.MFA.Factors(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := s.keepsAFactor(ctx, user, f.TOTP || f.Passkeys > 1); err != nil {
		return err
	}
	if err := s.store.MFA.DeletePasskey(ctx, user.ID, id); err != nil {
		return err
	}
	return s.dropCodesWithoutFactor(ctx, user.ID)
}

// BeginPasskeyLogin starts a sign-in with a passkey at rp. With a ticket it is the
// second step after the password and offers that user's passkeys; without, any passkey
// of the instance signs in on its own.
func (s *Service) BeginPasskeyLogin(ctx context.Context, rp RelyingParty, ticket string) (string, *protocol.CredentialAssertion, error) {
	wa, err := rp.webauthn()
	if err != nil {
		return "", nil, err
	}
	cer := &ceremony{rp: rp, ticket: ticket}
	var assertion *protocol.CredentialAssertion
	var session *webauthn.SessionData
	if ticket != "" {
		userID, err := s.ticketUser(ticket)
		if err != nil {
			return "", nil, err
		}
		u, err := s.loadWAUser(ctx, userID, rp)
		if err != nil {
			return "", nil, err
		}
		if len(u.creds) == 0 {
			return "", nil, fmt.Errorf("%w: none of this account's passkeys was registered at %s", validate.ErrInvalid, rp.ID)
		}
		cer.userID = userID
		assertion, session, err = wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return "", nil, err
		}
	} else {
		assertion, session, err = wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return "", nil, err
		}
	}
	cer.session = *session
	id, err := s.ceremonies.put(cer, s.now())
	if err != nil {
		return "", nil, err
	}
	return id, assertion, nil
}

// FinishPasskeyLogin checks the browser's answer and opens a session.
func (s *Service) FinishPasskeyLogin(ctx context.Context, id string, response []byte, ip, userAgent string) (string, store.User, error) {
	cer, err := s.ceremonies.take(id, s.now())
	if err != nil {
		return "", store.User{}, err
	}
	if cer.register {
		return "", store.User{}, ErrCeremony
	}
	limitKey := "passkey"
	if cer.userID != "" {
		limitKey = "mfa:" + cer.userID
	}
	if !s.limiter.allow(ip, limitKey, s.now()) {
		return "", store.User{}, ErrTooManyAttempts
	}
	fail := func(err error) (string, store.User, error) {
		s.limiter.fail(ip, limitKey, s.now())
		if cer.ticket != "" {
			s.ticketFailed(cer.ticket)
		}
		return "", store.User{}, fmt.Errorf("%w: %v", ErrPasskey, err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return fail(err)
	}
	wa, err := cer.rp.webauthn()
	if err != nil {
		return "", store.User{}, err
	}
	var u *waUser
	var cred *webauthn.Credential
	if cer.userID != "" {
		if u, err = s.loadWAUser(ctx, cer.userID, cer.rp); err != nil {
			return "", store.User{}, err
		}
		cred, err = wa.ValidateLogin(u, cer.session, parsed)
	} else {
		cred, err = wa.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
			found, err := s.loadWAUser(ctx, string(userHandle), cer.rp)
			if err != nil {
				return nil, err
			}
			if _, ok := found.rows[base64.RawURLEncoding.EncodeToString(rawID)]; !ok {
				return nil, errors.New("unknown passkey")
			}
			u = found
			return found, nil
		}, cer.session, parsed)
	}
	if err != nil {
		return fail(err)
	}
	if cred.Authenticator.CloneWarning {
		return fail(errors.New("the authenticator's counter went back: the passkey may have been copied"))
	}
	keyID := base64.RawURLEncoding.EncodeToString(cred.ID)
	row, ok := u.rows[keyID]
	if !ok || !bytes.Equal([]byte(row.UserID), []byte(u.user.ID)) {
		return fail(errors.New("unknown passkey"))
	}
	if u.user.Disabled {
		return fail(errors.New("the account is disabled"))
	}
	if raw, err := json.Marshal(cred); err == nil {
		if err := s.store.MFA.UsePasskey(ctx, keyID, string(raw)); err != nil {
			s.log.Warn("passkey: storing its counter", "err", err)
		}
	}
	if cer.ticket != "" {
		if err := s.finishTicket(ctx, cer.ticket); err != nil {
			return "", store.User{}, err
		}
	}
	s.limiter.reset(ip, limitKey)
	token, err := s.StartSession(ctx, u.user, MethodPasskey, ip, userAgent)
	return token, u.user, err
}

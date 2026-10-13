package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// The hoster of a managed instance can sign in as its support, but only while the
// instance's admins allow it: SettingSupportAccess holds that consent. The fleet manager
// asks the instance for a one-time login link (IssueSupportLogin); opening it starts a
// session of the support user (RedeemSupportLogin), which ends as soon as the consent
// does.

// SettingSupportAccess is the admins' consent: empty for none, "always" until revoked,
// or the RFC 3339 time it ends.
const SettingSupportAccess = "support_access"

// supportTicketTTL is how long a support login link works.
const supportTicketTTL = 2 * time.Minute

// maxSupportTickets is how many support login links may be open at once. A link is asked
// for right before it is used, so a few are plenty; more would let the fleet manager fill
// the memory with links.
const maxSupportTickets = 5

// maxSupportHoster is how much of the hoster's name a link keeps for the audit log.
const maxSupportHoster = 100

// ErrSupportTickets is returned when maxSupportTickets links are open already.
var ErrSupportTickets = errors.New("too many support login links are open; use one or wait two minutes until they expire")

// ErrSupportNotAllowed is returned when the admins did not allow support access.
var ErrSupportNotAllowed = errors.New("the admins of this instance don't allow support access")

// ErrSupportTicket is returned for an unknown, used or expired support login link.
var ErrSupportTicket = errors.New("the support login link is unknown, used or expired; open a new one from the fleet manager")

// SupportAccess is the admins' consent to the hoster's support.
type SupportAccess struct {
	Allowed bool `json:"allowed"`
	// Until is when the consent ends; zero while it holds until revoked.
	Until time.Time `json:"until,omitzero"`
}

// Active reports whether the consent holds at t.
func (a SupportAccess) Active(t time.Time) bool {
	return a.Allowed && (a.Until.IsZero() || t.Before(a.Until))
}

// supportTicket is a login link handed to the fleet manager.
type supportTicket struct {
	hoster  string
	expires time.Time
}

// SupportAccess returns the admins' consent; an expired one reads as none.
func (s *Service) SupportAccess(ctx context.Context) (SupportAccess, error) {
	v, err := s.store.Settings.Get(ctx, SettingSupportAccess)
	if errors.Is(err, store.ErrNotFound) || v == "" {
		return SupportAccess{}, nil
	}
	if err != nil {
		return SupportAccess{}, err
	}
	if v == "always" {
		return SupportAccess{Allowed: true}, nil
	}
	until, err := time.Parse(time.RFC3339, v)
	if err != nil || !s.now().Before(until) {
		return SupportAccess{}, nil
	}
	return SupportAccess{Allowed: true, Until: until}, nil
}

// SetSupportAccess stores the admins' consent. Taking it back ends the support's
// sessions at once.
func (s *Service) SetSupportAccess(ctx context.Context, a SupportAccess) error {
	v := ""
	switch {
	case a.Allowed && a.Until.IsZero():
		v = "always"
	case a.Allowed:
		if !s.now().Before(a.Until) {
			return fmt.Errorf("%w: the end of the support access must lie in the future", validate.ErrInvalid)
		}
		v = a.Until.UTC().Format(time.RFC3339)
	}
	if err := s.store.Settings.Set(ctx, SettingSupportAccess, v); err != nil {
		return err
	}
	if v == "" {
		return s.endSupportSessions(ctx)
	}
	return nil
}

func (s *Service) endSupportSessions(ctx context.Context) error {
	u, err := s.store.Users.Support(ctx)
	if err != nil {
		return err
	}
	return s.store.Sessions.DeleteByUser(ctx, u.ID)
}

// IssueSupportLogin returns a one-time login link token for the hoster's support, named
// hoster in the audit log. It fails without the admins' consent.
func (s *Service) IssueSupportLogin(ctx context.Context, hoster string) (string, error) {
	a, err := s.SupportAccess(ctx)
	if err != nil {
		return "", err
	}
	if !a.Active(s.now()) {
		return "", ErrSupportNotAllowed
	}
	if r := []rune(hoster); len(r) > maxSupportHoster {
		hoster = string(r[:maxSupportHoster])
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.supportMu.Lock()
	defer s.supportMu.Unlock()
	if s.supportTickets == nil {
		s.supportTickets = map[string]supportTicket{}
	}
	now := s.now()
	for k, t := range s.supportTickets {
		if now.After(t.expires) {
			delete(s.supportTickets, k)
		}
	}
	if len(s.supportTickets) >= maxSupportTickets {
		return "", ErrSupportTickets
	}
	s.supportTickets[hashToken(token)] = supportTicket{hoster: hoster, expires: now.Add(supportTicketTTL)}
	return token, nil
}

// RedeemSupportLogin turns a login link token into a session of the support user. It
// returns the session token, the user and the hoster who asked for the link.
func (s *Service) RedeemSupportLogin(ctx context.Context, token, ip, userAgent string) (string, store.User, string, error) {
	s.supportMu.Lock()
	t, ok := s.supportTickets[hashToken(token)]
	delete(s.supportTickets, hashToken(token))
	s.supportMu.Unlock()
	if !ok || s.now().After(t.expires) {
		return "", store.User{}, "", ErrSupportTicket
	}
	a, err := s.SupportAccess(ctx)
	if err != nil {
		return "", store.User{}, "", err
	}
	if !a.Active(s.now()) {
		return "", store.User{}, "", ErrSupportNotAllowed
	}
	u, err := s.store.Users.Support(ctx)
	if err != nil {
		return "", store.User{}, "", err
	}
	session, err := s.StartSession(ctx, u, ip, userAgent)
	return session, u, t.hoster, err
}

package project

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A guest link lets someone without an Envoryx account see a project that signs in
// through Envoryx, such as the customer of an agency reviewing the staging site. It
// works on the project's own host names (not its addons) until it ends or is revoked.
const (
	guestLinkMaxDays    = proxy.GuestLinkMaxDays
	guestLinkDefaultDay = 7
	maxGuestLinks       = 50
)

// GuestLink is a guest link as the UI shows it. URL carries the token and is only set
// right after the link is made.
type GuestLink struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	URL       string     `json:"url,omitempty"`
}

func toGuestLink(l store.GuestLink) GuestLink {
	return GuestLink{ID: l.ID, Label: l.Label, CreatedBy: l.CreatedBy, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, UsedAt: l.UsedAt}
}

// GuestLinks lists a project's guest links that still work.
func (m *Manager) GuestLinks(ctx context.Context, id string) ([]GuestLink, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, id); err != nil {
		return nil, err
	}
	_, _ = m.store.Guests.DeleteExpired(ctx, time.Now().UTC())
	links, err := m.store.Guests.ListByProject(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []GuestLink{}
	for _, l := range links {
		out = append(out, toGuestLink(l))
	}
	return out, nil
}

// CreateGuestLink makes a link that works for days (1 to 90; 0 means a week).
func (m *Manager) CreateGuestLink(ctx context.Context, id, label string, days int) (GuestLink, error) {
	if err := validate.UUID(id); err != nil {
		return GuestLink{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return GuestLink{}, err
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 80 || strings.ContainsAny(label, "\r\n") {
		return GuestLink{}, invalid("name the link in 1 to 80 characters, say who it is for")
	}
	if days == 0 {
		days = guestLinkDefaultDay
	}
	if days < 1 || days > guestLinkMaxDays {
		return GuestLink{}, invalid("a guest link works for 1 to %d days", guestLinkMaxDays)
	}
	// The link goes to the project's host name, where the proxy takes it.
	base := ""
	if planner, err := m.planner(); err == nil && planner.paths.BaseDomain != "" {
		base = planner.proxyURL(DefaultHostname(p.Slug, planner.paths.BaseDomain))
	}
	if base == "" {
		return GuestLink{}, fmt.Errorf("%w: guest links need the embedded proxy and a base domain", ErrConflict)
	}
	existing, err := m.store.Guests.ListByProject(ctx, id)
	if err != nil {
		return GuestLink{}, err
	}
	if len(existing) >= maxGuestLinks {
		return GuestLink{}, fmt.Errorf("%w: a project has at most %d guest links; revoke one first", ErrConflict, maxGuestLinks)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return GuestLink{}, err
	}
	token := proxy.GuestTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	by := "envoryx"
	if pr, ok := auth.PrincipalFrom(ctx); ok {
		by = audit.Actor(pr)
	}
	l := store.GuestLink{ID: store.NewID(), ProjectID: id, Label: label, TokenHash: hashGuestToken(token), CreatedBy: by, CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)}
	if err := m.store.Guests.Create(ctx, l); err != nil {
		return GuestLink{}, err
	}
	m.audit.Log(ctx, "guest_link.created", "project", id, map[string]any{"name": p.Name, "label": label, "expiresAt": l.ExpiresAt})
	out := toGuestLink(l)
	out.URL = base + proxy.GuestPath + "?t=" + token
	return out, nil
}

// RevokeGuestLink ends a link; browsers that came in with it are out within half a
// minute.
func (m *Manager) RevokeGuestLink(ctx context.Context, id, linkID string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := validate.UUID(linkID); err != nil {
		return ErrNotFound
	}
	l, err := m.store.Guests.Get(ctx, linkID)
	if err != nil {
		return err
	}
	if err := m.store.Guests.Delete(ctx, id, linkID); err != nil {
		return err
	}
	m.audit.Log(ctx, "guest_link.revoked", "project", id, map[string]any{"label": l.Label})
	return nil
}

// GuestPass checks a guest token for the proxy: the project its link opens, while the
// link works. It notes the use, at most once a minute.
func (m *Manager) GuestPass(ctx context.Context, token string) (projectID string, ok bool) {
	if !strings.HasPrefix(token, proxy.GuestTokenPrefix) || len(token) > 100 {
		return "", false
	}
	l, err := m.store.Guests.ByHash(ctx, hashGuestToken(token))
	now := time.Now().UTC()
	if err != nil || !now.Before(l.ExpiresAt) || m.Suspension().Err() != nil {
		return "", false
	}
	if l.UsedAt == nil || now.Sub(*l.UsedAt) > time.Minute {
		_ = m.store.Guests.Touch(ctx, l.ID, now)
	}
	return l.ProjectID, true
}

func hashGuestToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

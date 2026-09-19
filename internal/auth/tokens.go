package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/envoryx/envoryx/internal/store"
)

// TokenPrefix marks Envoryx API tokens so they are recognisable in configs and logs.
const TokenPrefix = "stq_"

// ErrInvalidTokenName is returned for unusable token names.
var ErrInvalidTokenName = errors.New("token name must be 1-64 printable characters")

// TokenSpec describes the token to issue.
type TokenSpec struct {
	Name  string
	Scope Scope
	// Projects confines the token to these project ids (validated by the caller); empty
	// means all projects.
	Projects []string
}

// CreateAPIToken issues a new bearer token for the principal's user. The plain token is
// returned exactly once; only its hash is stored.
func (s *Service) CreateAPIToken(ctx context.Context, p Principal, spec TokenSpec) (string, store.APIToken, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" || len(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) }) {
		return "", store.APIToken{}, ErrInvalidTokenName
	}
	scope, err := ParseScope(string(spec.Scope))
	if err != nil {
		return "", store.APIToken{}, err
	}
	projects := slices.Clone(spec.Projects)
	slices.Sort(projects)
	projects = slices.Compact(projects)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", store.APIToken{}, fmt.Errorf("generate token: %w", err)
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	t := store.APIToken{
		ID:         store.NewID(),
		UserID:     p.UserID,
		Name:       name,
		TokenHash:  hashToken(token),
		Prefix:     token[:len(TokenPrefix)+6],
		Scope:      string(scope),
		ProjectIDs: projects,
		CreatedAt:  s.now(),
	}
	if err := s.store.Tokens.Create(ctx, t); err != nil {
		return "", store.APIToken{}, err
	}
	return token, t, nil
}

// ValidateAPIToken resolves a bearer token into a principal.
func (s *Service) ValidateAPIToken(ctx context.Context, token string) (Principal, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, TokenPrefix) || len(token) < len(TokenPrefix)+20 {
		return Principal{}, ErrUnauthenticated
	}
	t, err := s.store.Tokens.GetByHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	user, err := s.store.Users.ByID(ctx, t.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	ts := s.now()
	if t.LastUsedAt == nil || ts.Sub(*t.LastUsedAt) > time.Minute {
		if err := s.store.Tokens.Touch(ctx, t.ID, ts); err != nil {
			s.log.Warn("touch api token failed", "err", err)
		}
	}
	return Principal{UserID: user.ID, Username: user.Username, Role: user.Role, TokenName: t.Name, Scope: Scope(t.Scope), Projects: t.ProjectIDs}, nil
}

// ListAPITokens returns all tokens (without hashes being useful to anyone).
func (s *Service) ListAPITokens(ctx context.Context) ([]store.APIToken, error) {
	return s.store.Tokens.List(ctx)
}

// RevokeAPIToken deletes a token.
func (s *Service) RevokeAPIToken(ctx context.Context, id string) error {
	return s.store.Tokens.Delete(ctx, id)
}

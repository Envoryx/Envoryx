package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

type ctxKey struct{}

// PrincipalFrom returns the principal stored in the context, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WithPrincipal stores a principal in the context (used by tests and the middleware).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// TokenFromRequest extracts the raw session token from the cookie.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// Middleware validates the session cookie and injects the principal. Requests without a
// valid session are rejected with the supplied unauthorized handler.
func (s *Service) Middleware(unauthorized http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := s.Validate(r.Context(), TokenFromRequest(r))
			if err != nil {
				if !errors.Is(err, ErrUnauthenticated) {
					s.log.Error("session validation failed", "err", err)
				}
				unauthorized.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// ClientIP returns the remote IP of a request. Proxy headers are intentionally ignored
// unless Envoryx is explicitly told it runs behind a trusted proxy (future setting).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

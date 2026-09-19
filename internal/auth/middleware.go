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

// BearerToken returns the API token carried in the Authorization header, or "" when the
// request has no bearer credential. The token is not validated here.
func BearerToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(raw) < 7 || !strings.EqualFold(raw[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(raw[7:])
}

// Middleware authenticates the request and injects the principal. A bearer API token in
// the Authorization header wins over the session cookie; a request that presents a bearer
// token that does not validate is rejected without falling back to the cookie, so a stray
// or revoked token never silently continues as the browser session. Requests without a
// valid credential are rejected with the supplied unauthorized handler.
func (s *Service) Middleware(unauthorized http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				p   Principal
				err error
			)
			if bearer := BearerToken(r); bearer != "" {
				p, err = s.ValidateAPIToken(r.Context(), bearer)
				if err != nil && !errors.Is(err, ErrUnauthenticated) {
					s.log.Error("api token validation failed", "err", err)
				}
			} else {
				p, err = s.Validate(r.Context(), TokenFromRequest(r))
				if err != nil && !errors.Is(err, ErrUnauthenticated) {
					s.log.Error("session validation failed", "err", err)
				}
			}
			if err != nil {
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

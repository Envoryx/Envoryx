package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Rules are what the proxy does with a project's requests before they reach the
// application, in this order: the address allowlist, CORS preflight answers, basic
// authentication and redirects; response headers and CORS headers go on the answer.
type Rules struct {
	// Allow admits only these networks (empty = every address).
	Allow []netip.Prefix
	// BasicAuth asks for credentials (nil = none).
	BasicAuth *BasicAuth
	Redirects []Redirect
	// Headers are set on every proxied response; an empty value removes the header.
	Headers []Header
	CORS    *CORS
}

// BasicAuth checks credentials against a bcrypt hash.
type BasicAuth struct {
	User  string
	Hash  string
	Realm string
}

// Redirect answers a path (or a prefix ending in "*") with a redirect to To; a To
// ending in "*" gets the rest of the path.
type Redirect struct {
	Host   string
	From   string
	To     string
	Status int
}

// Header is a response header.
type Header struct{ Name, Value string }

// CORS lets pages on the listed origins call the project.
type CORS struct {
	// Origins are exact origins, "scheme://*.domain" wildcards or "*".
	Origins     []string
	Methods     []string
	Headers     []string
	Credentials bool
	MaxAgeSec   int
}

// Empty reports rules that do nothing.
func (r *Rules) Empty() bool {
	return r == nil || (len(r.Allow) == 0 && r.BasicAuth == nil && len(r.Redirects) == 0 && len(r.Headers) == 0 && r.CORS == nil)
}

// allowed reports whether the client's address may reach the project.
func (r *Rules) allowed(req *http.Request) (netip.Addr, bool) {
	addr := clientAddr(req)
	if len(r.Allow) == 0 {
		return addr, true
	}
	if !addr.IsValid() {
		return addr, false
	}
	for _, p := range r.Allow {
		if p.Contains(addr) {
			return addr, true
		}
	}
	return addr, false
}

func clientAddr(req *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// redirect returns the address a request is sent to, if a rule matches.
func (r *Rules) redirect(req *http.Request) (string, int, bool) {
	host := hostOf(req)
	for _, rd := range r.Redirects {
		if rd.Host != "" && rd.Host != host {
			continue
		}
		rest, ok := "", req.URL.Path == rd.From
		if prefix, wild := strings.CutSuffix(rd.From, "*"); wild {
			rest, ok = strings.CutPrefix(req.URL.Path, prefix)
		}
		if !ok {
			continue
		}
		to := rd.To
		if base, wild := strings.CutSuffix(to, "*"); wild {
			to = base + rest
		}
		if req.URL.RawQuery != "" && !strings.Contains(to, "?") {
			to += "?" + req.URL.RawQuery
		}
		status := rd.Status
		if status == 0 {
			status = http.StatusFound
		}
		return to, status, true
	}
	return "", 0, false
}

// corsOrigin returns the value of Access-Control-Allow-Origin for the request's origin
// ("" = not allowed).
func (c *CORS) corsOrigin(origin string) string {
	if origin == "" {
		return ""
	}
	for _, o := range c.Origins {
		switch {
		case o == "*":
			if c.Credentials {
				return origin
			}
			return "*"
		case strings.EqualFold(o, origin):
			return origin
		case strings.Contains(o, "://*."):
			scheme, suffix, _ := strings.Cut(o, "://*")
			if s, rest, ok := strings.Cut(origin, "://"); ok && strings.EqualFold(s, scheme) && strings.HasSuffix(strings.ToLower(rest), strings.ToLower(suffix)) {
				return origin
			}
		}
	}
	return ""
}

func (c *CORS) methods() string {
	if len(c.Methods) == 0 {
		return "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	}
	return strings.Join(c.Methods, ", ")
}

// preflight answers a CORS preflight request for an allowed origin.
func (c *CORS) preflight(w http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodOptions || req.Header.Get("Access-Control-Request-Method") == "" {
		return false
	}
	allow := c.corsOrigin(req.Header.Get("Origin"))
	if allow == "" {
		return false
	}
	h := w.Header()
	c.setOrigin(h, allow)
	h.Set("Access-Control-Allow-Methods", c.methods())
	if len(c.Headers) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(c.Headers, ", "))
	} else if asked := req.Header.Get("Access-Control-Request-Headers"); asked != "" {
		h.Set("Access-Control-Allow-Headers", asked)
		h.Add("Vary", "Access-Control-Request-Headers")
	}
	if c.MaxAgeSec > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAgeSec))
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (c *CORS) setOrigin(h http.Header, allow string) {
	h.Set("Access-Control-Allow-Origin", allow)
	if allow != "*" {
		h.Add("Vary", "Origin")
	}
	if c.Credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
}

// respond applies the response headers and CORS to a proxied answer.
func (r *Rules) respond(resp *http.Response) {
	for _, hd := range r.Headers {
		if hd.Value == "" {
			resp.Header.Del(hd.Name)
		} else {
			resp.Header.Set(hd.Name, hd.Value)
		}
	}
	if r.CORS != nil {
		if allow := r.CORS.corsOrigin(resp.Request.Header.Get("Origin")); allow != "" {
			resp.Header.Del("Access-Control-Allow-Origin")
			resp.Header.Del("Access-Control-Allow-Credentials")
			r.CORS.setOrigin(resp.Header, allow)
		}
	}
}

// credentialCache remembers credentials that passed a bcrypt check, so a page with fifty
// assets costs one check and not fifty.
type credentialCache struct {
	mu sync.Mutex
	ok map[[32]byte]bool
}

func (c *credentialCache) check(a *BasicAuth, user, pass string) bool {
	if subtle.ConstantTimeCompare([]byte(user), []byte(a.User)) != 1 {
		return false
	}
	key := sha256.Sum256([]byte(a.Hash + "\x00" + user + "\x00" + pass))
	c.mu.Lock()
	hit := c.ok[key]
	c.mu.Unlock()
	if hit {
		return true
	}
	if bcrypt.CompareHashAndPassword([]byte(a.Hash), []byte(pass)) != nil {
		return false
	}
	c.mu.Lock()
	if c.ok == nil || len(c.ok) > 1000 {
		c.ok = map[[32]byte]bool{}
	}
	c.ok[key] = true
	c.mu.Unlock()
	return true
}

// guard applies the rules that may answer a request instead of the application; it
// reports whether it answered.
func (h *Handler) guard(w http.ResponseWriter, req *http.Request, r *Rules) bool {
	if r.CORS != nil && r.CORS.preflight(w, req) {
		return true
	}
	if a := r.BasicAuth; a != nil {
		user, pass, ok := req.BasicAuth()
		if !ok || !h.creds.check(a, user, pass) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", a.Realm))
			h.errorPage(w, http.StatusUnauthorized, "Sign in required", fmt.Sprintf("<strong>%s</strong> asks for a user name and password.", html.EscapeString(a.Realm)), "")
			return true
		}
		// The application gets no credentials that were meant for the proxy.
		req.Header.Del("Authorization")
	}
	if to, status, ok := r.redirect(req); ok {
		http.Redirect(w, req, to, status)
		return true
	}
	return false
}

type rulesKey struct{}

func withRules(req *http.Request, r *Rules) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), rulesKey{}, r))
}

func rulesOf(ctx context.Context) *Rules {
	r, _ := ctx.Value(rulesKey{}).(*Rules)
	return r
}

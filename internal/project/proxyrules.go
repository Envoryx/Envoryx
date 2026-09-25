package project

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Limits of the proxy rules, so one project cannot make every request slow.
const (
	maxAllowIPs   = 100
	maxRedirects  = 50
	maxHeaders    = 30
	maxCORSValues = 30
)

// ProxyRulesRequest changes a project's proxy rules; it replaces all of them.
type ProxyRulesRequest struct {
	AllowIPs  []string             `json:"allowIPs"`
	BasicAuth *BasicAuthRequest    `json:"basicAuth"`
	Redirects []store.RedirectRule `json:"redirects"`
	Headers   []store.HeaderRule   `json:"headers"`
	CORS      *store.CORSRule      `json:"cors"`
}

// BasicAuthRequest sets basic authentication. An empty password keeps the one stored.
type BasicAuthRequest struct {
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
}

var (
	headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	methodRe     = regexp.MustCompile(`^[A-Z]+$`)
)

// Headers the proxy or HTTP itself owns.
var reservedHeaders = []string{"Connection", "Content-Length", "Content-Encoding", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Www-Authenticate"}

// SetProxyRules replaces a project's proxy rules.
func (m *Manager) SetProxyRules(ctx context.Context, id string, req ProxyRulesRequest) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	rules, err := normalizeProxyRules(req, p.ProxyRules)
	if err != nil {
		return View{}, err
	}
	if err := m.store.Projects.SetProxyRules(ctx, id, rules); err != nil {
		return View{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"proxyRules": auditRules(rules)}})
	return m.Get(ctx, id)
}

// auditRules is what the audit log keeps of the rules: no password hash.
func auditRules(r store.ProxyRules) store.ProxyRules {
	if r.BasicAuth != nil {
		r.BasicAuth = &store.BasicAuthRule{User: r.BasicAuth.User}
	}
	return r
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{validate.ErrInvalid}, args...)...)
}

// normalizeProxyRules checks the rules and brings them into their stored form; prev
// provides the password hash kept when no new password is given.
func normalizeProxyRules(req ProxyRulesRequest, prev store.ProxyRules) (store.ProxyRules, error) {
	var out store.ProxyRules
	if len(req.AllowIPs) > maxAllowIPs {
		return out, invalid("at most %d addresses", maxAllowIPs)
	}
	for _, s := range req.AllowIPs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pfx, err := parsePrefix(s)
		if err != nil {
			return out, invalid("%q is no address or network", s)
		}
		if !slices.Contains(out.AllowIPs, pfx.String()) {
			out.AllowIPs = append(out.AllowIPs, pfx.String())
		}
	}

	if a := req.BasicAuth; a != nil {
		user := strings.TrimSpace(a.User)
		if user == "" || len(user) > 64 || strings.ContainsAny(user, ":\r\n\x00") {
			return out, invalid("the user name has 1 to 64 characters and no colon")
		}
		rule := &store.BasicAuthRule{User: user}
		switch {
		case a.Password != "":
			if len(a.Password) > 72 {
				return out, invalid("the password has at most 72 bytes")
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcrypt.DefaultCost)
			if err != nil {
				return out, err
			}
			rule.PasswordHash = string(hash)
		case prev.BasicAuth != nil && prev.BasicAuth.PasswordHash != "":
			rule.PasswordHash = prev.BasicAuth.PasswordHash
		default:
			return out, invalid("basic authentication needs a password")
		}
		out.BasicAuth = rule
	}

	if len(req.Redirects) > maxRedirects {
		return out, invalid("at most %d redirects", maxRedirects)
	}
	for i, r := range req.Redirects {
		r.Host = strings.TrimSpace(r.Host)
		if r.Host != "" {
			if r.Host = validate.NormalizeHostname(r.Host); r.Host == "" {
				return out, invalid("redirect %d: the host name is invalid", i+1)
			}
		}
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		if !strings.HasPrefix(r.From, "/") || strings.ContainsAny(r.From, " ?#") || strings.Contains(strings.TrimSuffix(r.From, "*"), "*") {
			return out, invalid("redirect %d: the source is a path such as /old or /old/*", i+1)
		}
		if err := checkRedirectTarget(r.To); err != nil {
			return out, invalid("redirect %d: %v", i+1, err)
		}
		if r.To == r.From {
			return out, invalid("redirect %d leads to itself", i+1)
		}
		switch r.Status {
		case 0:
			r.Status = http.StatusFound
		case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		default:
			return out, invalid("redirect %d: the status is 301, 302, 307 or 308", i+1)
		}
		out.Redirects = append(out.Redirects, r)
	}

	if len(req.Headers) > maxHeaders {
		return out, invalid("at most %d headers", maxHeaders)
	}
	for _, h := range req.Headers {
		name := strings.TrimSpace(h.Name)
		if !headerNameRe.MatchString(name) {
			return out, invalid("%q is no header name", name)
		}
		name = http.CanonicalHeaderKey(name)
		if slices.Contains(reservedHeaders, name) || strings.HasPrefix(name, "Access-Control-") {
			return out, invalid("the proxy sets %s itself", name)
		}
		if slices.ContainsFunc(out.Headers, func(o store.HeaderRule) bool { return o.Name == name }) {
			return out, invalid("%s is there twice", name)
		}
		value := strings.TrimSpace(h.Value)
		if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
			return out, invalid("the value of %s is one line of at most 2048 characters", name)
		}
		out.Headers = append(out.Headers, store.HeaderRule{Name: name, Value: value})
	}

	if c := req.CORS; c != nil && len(c.Origins) > 0 {
		cors, err := normalizeCORS(*c)
		if err != nil {
			return out, err
		}
		out.CORS = &cors
	}
	return out, nil
}

// parsePrefix reads an address ("10.0.0.5") or a network ("10.0.0.0/8").
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func checkRedirectTarget(to string) error {
	if to == "" || strings.ContainsAny(to, " \r\n") || strings.Contains(strings.TrimSuffix(to, "*"), "*") {
		return fmt.Errorf("the target is a path or an http(s) address")
	}
	if strings.HasPrefix(to, "/") {
		if strings.HasPrefix(to, "//") {
			return fmt.Errorf("the target is a path or an http(s) address")
		}
		return nil
	}
	u, err := url.Parse(strings.TrimSuffix(to, "*"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the target is a path or an http(s) address")
	}
	return nil
}

func normalizeCORS(c store.CORSRule) (store.CORSRule, error) {
	out := store.CORSRule{Credentials: c.Credentials, MaxAgeSec: c.MaxAgeSec}
	if len(c.Origins) > maxCORSValues || len(c.Methods) > maxCORSValues || len(c.Headers) > maxCORSValues {
		return out, invalid("at most %d CORS origins, methods and headers each", maxCORSValues)
	}
	for _, o := range c.Origins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		if o == "*" {
			if c.Credentials {
				return out, invalid("credentials need the origins listed, not *")
			}
		} else {
			u, err := url.Parse(strings.Replace(o, "://*.", "://wildcard.", 1))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
				return out, invalid("%q is no origin such as https://app.test", o)
			}
			o = strings.ToLower(o)
		}
		if !slices.Contains(out.Origins, o) {
			out.Origins = append(out.Origins, o)
		}
	}
	if len(out.Origins) == 0 {
		return out, invalid("CORS needs at least one origin")
	}
	for _, m := range c.Methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if !methodRe.MatchString(m) {
			return out, invalid("%q is no HTTP method", m)
		}
		if !slices.Contains(out.Methods, m) {
			out.Methods = append(out.Methods, m)
		}
	}
	for _, h := range c.Headers {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h != "*" && !headerNameRe.MatchString(h) {
			return out, invalid("%q is no header name", h)
		}
		out.Headers = append(out.Headers, h)
	}
	if out.MaxAgeSec < 0 || out.MaxAgeSec > 86400 {
		return out, invalid("the preflight cache lasts 0 to 86400 seconds")
	}
	return out, nil
}

// proxyRules compiles a project's stored rules for the proxy (nil = none).
func proxyRules(p store.Project) *proxy.Rules {
	r := p.ProxyRules
	if r.Empty() {
		return nil
	}
	out := &proxy.Rules{}
	for _, s := range r.AllowIPs {
		if pfx, err := parsePrefix(s); err == nil {
			out.Allow = append(out.Allow, pfx)
		}
	}
	if a := r.BasicAuth; a != nil && a.PasswordHash != "" {
		out.BasicAuth = &proxy.BasicAuth{User: a.User, Hash: a.PasswordHash, Realm: p.Name}
	}
	for _, rd := range r.Redirects {
		out.Redirects = append(out.Redirects, proxy.Redirect{Host: rd.Host, From: rd.From, To: rd.To, Status: rd.Status})
	}
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, proxy.Header{Name: h.Name, Value: h.Value})
	}
	if c := r.CORS; c != nil {
		out.CORS = &proxy.CORS{Origins: c.Origins, Methods: c.Methods, Headers: c.Headers, Credentials: c.Credentials, MaxAgeSec: c.MaxAgeSec}
	}
	return out
}

package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// The gate keeps a host behind the Envoryx sign-in. A browser without a pass goes to
// Envoryx's /gate page, which checks the session and the user's role in the project
// and sends it back with a one-time code; the proxy swaps the code at GatePath for a
// cookie of that host alone. The host can't share the Envoryx session cookie: it may
// be a domain of its own, and its application must not see the session.
const (
	// GatePath is where a host behind the gate takes the code; it never reaches the
	// application.
	GatePath    = "/.envoryx/gate"
	gateCookie  = "envoryx_gate"
	gateCodeTTL = time.Minute
	// gatePassTTL ends a pass even while its session goes on; the next request then
	// takes the round trip through Envoryx again, which a signed-in user doesn't notice.
	gatePassTTL = 12 * time.Hour
	// gateRecheck is how long a pass trusts its last look at the session: signing out,
	// a disabled user or a lost role close the host within this time.
	gateRecheck    = 30 * time.Second
	gateMaxEntries = 10000
)

// ErrNotGated is returned for an address that isn't behind the gate.
var ErrNotGated = errors.New("this address isn't behind the Envoryx sign-in")

// GateCheck reports whether the session still exists and its user holds need ("read",
// "operate") in the project.
type GateCheck func(ctx context.Context, sessionID, projectID, need string) bool

// Gate hands out and checks the passes of the hosts behind the gate.
type Gate struct {
	check GateCheck
	now   func() time.Time

	mu     sync.Mutex
	router *Router
	codes  map[string]gateEntry // by code
	passes map[[32]byte]*gateEntry
}

type gateEntry struct {
	sessionID, host, projectID, need string
	// path is where the browser goes once the code is taken, after the hosts in rest,
	// which get a pass on the way; scheme and port are the address's (codes only).
	path         string
	rest         []string
	scheme, port string
	checked      time.Time
	expires      time.Time
}

// NewGate creates a gate that asks check about the sessions behind its passes.
func NewGate(check GateCheck) *Gate {
	return &Gate{check: check, now: time.Now, codes: map[string]gateEntry{}, passes: map[[32]byte]*gateEntry{}}
}

// SetRouter hands the gate the routing table it looks addresses up in.
func (g *Gate) SetRouter(r *Router) {
	g.mu.Lock()
	g.router = r
	g.mu.Unlock()
}

// Resolve finds the target behind the gate that an address leads to.
func (g *Gate) Resolve(ctx context.Context, raw string) (*url.URL, Target, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, Target{}, ErrNotGated
	}
	g.mu.Lock()
	router := g.router
	g.mu.Unlock()
	if router == nil {
		return nil, Target{}, ErrNotGated
	}
	t, ok := router.Table(ctx).Routes[strings.ToLower(u.Hostname())]
	if !ok || t.Login == "" {
		return nil, Target{}, ErrNotGated
	}
	return u, t, nil
}

// Issue gives a session a code for the target at u and returns the address that takes
// it there. The caller has checked that the session's user may enter.
func (g *Gate) Issue(ctx context.Context, sessionID string, u *url.URL, t Target) (string, error) {
	path := u.EscapedPath()
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || path == GatePath {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	host := strings.ToLower(u.Hostname())
	// The hosts the target's pages load from (its dev server) get a pass on the way, or
	// their scripts would be refused: the browser passes each of them before it lands.
	hops := append(g.companions(ctx, host, t), host)
	e := gateEntry{sessionID: sessionID, host: hops[0], rest: hops[1:], projectID: t.ProjectID, need: t.Login, path: path, scheme: u.Scheme, port: u.Port()}
	return g.issue(e)
}

// issue stores a code for the entry's host and returns the address that redeems it.
func (g *Gate) issue(e gateEntry) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	now := g.now()
	e.expires = now.Add(gateCodeTTL)
	g.mu.Lock()
	g.prune(now)
	g.codes[code] = e
	g.mu.Unlock()
	host := e.host
	if e.port != "" {
		host = net.JoinHostPort(host, e.port)
	}
	back := url.URL{Scheme: e.scheme, Host: host, Path: GatePath, RawQuery: url.Values{"code": {code}}.Encode()}
	return back.String(), nil
}

// companions are the hosts of t.LoginAlso behind the same gate of the same project.
func (g *Gate) companions(ctx context.Context, host string, t Target) []string {
	if len(t.LoginAlso) == 0 {
		return nil
	}
	g.mu.Lock()
	router := g.router
	g.mu.Unlock()
	if router == nil {
		return nil
	}
	routes := router.Table(ctx).Routes
	var out []string
	for _, h := range t.LoginAlso {
		h = strings.ToLower(h)
		if c, ok := routes[h]; ok && h != host && c.ProjectID == t.ProjectID && c.Login == t.Login && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// redeemGate swaps the code in the request for a pass of the host and sends the browser on.
func (h *Handler) redeemGate(w http.ResponseWriter, r *http.Request, host string, t Target) {
	g := h.gate
	code := r.URL.Query().Get("code")
	now := g.now()
	g.mu.Lock()
	e, ok := g.codes[code]
	delete(g.codes, code)
	g.mu.Unlock()
	if !ok || now.After(e.expires) || e.host != host || e.projectID != t.ProjectID || e.need != t.Login {
		h.errorPage(w, http.StatusForbidden, "Sign-in link expired",
			`This sign-in link is used up or too old. <a href="/">Open the page again</a> to get a new one.`, "")
		return
	}
	token, err := randomToken()
	if err != nil {
		h.errorPage(w, http.StatusInternalServerError, "Sign-in failed", "Envoryx could not create the sign-in.", "")
		return
	}
	pass := &gateEntry{sessionID: e.sessionID, host: e.host, projectID: e.projectID, need: e.need, checked: now, expires: now.Add(gatePassTTL)}
	g.mu.Lock()
	g.passes[sha256.Sum256([]byte(token))] = pass
	g.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: gateCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	// The code is in this address; it must not travel on in a Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	to := e.path
	if len(e.rest) > 0 {
		next := e
		next.host, next.rest = e.rest[0], e.rest[1:]
		if to, err = g.issue(next); err != nil {
			h.errorPage(w, http.StatusInternalServerError, "Sign-in failed", "Envoryx could not create the sign-in.", "")
			return
		}
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// admitGate reports whether the request carries a valid pass for the target.
func (h *Handler) admitGate(r *http.Request, host string, t Target) bool {
	g := h.gate
	c, err := r.Cookie(gateCookie)
	if err != nil || c.Value == "" {
		return false
	}
	key := sha256.Sum256([]byte(c.Value))
	now := g.now()
	g.mu.Lock()
	p, ok := g.passes[key]
	if ok && now.After(p.expires) {
		delete(g.passes, key)
		ok = false
	}
	var pass gateEntry
	if ok {
		pass = *p
	}
	g.mu.Unlock()
	if !ok || pass.host != host || pass.projectID != t.ProjectID || pass.need != t.Login {
		return false
	}
	if now.Sub(pass.checked) < gateRecheck {
		return true
	}
	if !g.check(r.Context(), pass.sessionID, pass.projectID, pass.need) {
		g.mu.Lock()
		delete(g.passes, key)
		g.mu.Unlock()
		return false
	}
	g.mu.Lock()
	if p, ok := g.passes[key]; ok {
		p.checked = now
	}
	g.mu.Unlock()
	return true
}

// challengeGate sends a browser without a pass to Envoryx's sign-in; requests a browser
// doesn't navigate to (scripts, WebSockets) get a plain refusal.
func (h *Handler) challengeGate(w http.ResponseWriter, r *http.Request, t Target, envoryxURL string) {
	navigate := r.Method == http.MethodGet || r.Method == http.MethodHead
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		navigate = navigate && mode == "navigate"
	} else {
		navigate = navigate && strings.Contains(r.Header.Get("Accept"), "text/html")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	here := url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	if envoryxURL == "" {
		h.errorPage(w, http.StatusUnauthorized, "Sign in required", "<strong>"+html.EscapeString(t.ProjectName)+"</strong> is only open to users signed in to Envoryx.", "")
		return
	}
	signIn := strings.TrimSuffix(envoryxURL, "/") + "/gate?to=" + url.QueryEscape(here.String())
	if navigate {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, signIn, http.StatusFound)
		return
	}
	h.errorPage(w, http.StatusUnauthorized, "Sign in required",
		"<strong>"+html.EscapeString(t.ProjectName)+`</strong> is only open to users signed in to Envoryx. <a href="`+html.EscapeString(signIn)+`">Sign in</a>`, "")
}

// dropGateCookie keeps the pass from the application: it is the proxy's, not the app's.
func dropGateCookie(r *http.Request) {
	if _, err := r.Cookie(gateCookie); err != nil {
		return
	}
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != gateCookie {
			r.AddCookie(c)
		}
	}
}

// prune drops expired codes and passes once the maps grow; g.mu is held.
func (g *Gate) prune(now time.Time) {
	for k, e := range g.codes {
		if now.After(e.expires) {
			delete(g.codes, k)
		}
	}
	if len(g.passes) < gateMaxEntries {
		return
	}
	for k, e := range g.passes {
		if now.After(e.expires) {
			delete(g.passes, k)
		}
	}
	// Still full: some passes go at random, their browsers just sign in again.
	for k := range g.passes {
		if len(g.passes) < gateMaxEntries {
			break
		}
		delete(g.passes, k)
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

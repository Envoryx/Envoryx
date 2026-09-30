package api

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/project"
)

// dbToolStatus reports the database browser (Settings).
func (a *API) dbToolStatus(w http.ResponseWriter, r *http.Request) {
	st, err := a.d.Projects.DBToolStatus(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// setDBTool switches the database browser on or off.
func (a *API) setDBTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.SetDBToolEnabled(r.Context(), req.Enabled); err != nil {
		writeError(w, r, err)
		return
	}
	a.dbToolStatus(w, r)
}

// openDBTool prepares the browser for a project's database and returns the link.
func (a *API) openDBTool(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	link, err := a.d.Projects.OpenDBTool(r.Context(), r.PathValue("id"), db)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// dbToolProxy serves the browser under /dbtool/ inside the Envoryx UI. The prefix is
// stripped for the container (Adminer builds relative links, so the pages work under any
// path); absolute Location headers get it back.
//
// The one Adminer container holds the logins of every project, so the route guard (operate
// in any project) is only the door: each request's target is checked against the
// principal's own projects before it is forwarded, on every request, because Adminer keeps
// its logins in a session that outlives the Envoryx user who made them.
type dbToolProxy struct {
	projects *project.Manager
	mu       sync.Mutex
	proxies  map[string]*httputil.ReverseProxy
}

func newDBToolProxy(projects *project.Manager) *dbToolProxy {
	return &dbToolProxy{projects: projects, proxies: map[string]*httputil.ReverseProxy{}}
}

func (p *dbToolProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, login, err := adminerTargets(r)
	if err == nil && target != nil {
		err = p.authorize(r, *target)
	}
	if err == nil && login != nil {
		err = p.authorize(r, *login)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	dial, err := p.projects.DBToolDial(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if dial == "" {
		http.Error(w, "The database browser is not running. Open a database from a project's Database section to start it.", http.StatusServiceUnavailable)
		return
	}
	token, err := p.projects.DBToolProxyToken()
	if err != nil {
		writeError(w, r, err)
		return
	}
	// Set, never passed on: the container trusts these headers from the proxy only.
	r.Header.Set(project.DBToolTokenHeader, token)
	r.Header.Del(project.DBToolTargetHeader)
	if target != nil {
		r.Header.Set(project.DBToolTargetHeader, target.Key())
	}
	p.proxyFor(dial).ServeHTTP(w, r)
}

// errDBToolTarget is the refusal for a target the principal may not open.
var errDBToolTarget = fmt.Errorf("%w: the database browser opens only databases of projects you may operate", auth.ErrForbidden)

// authorize lets a target through when it belongs to a project the principal may operate.
// A target that belongs to no project is refused too, so the browser cannot be pointed at
// arbitrary hosts.
func (p *dbToolProxy) authorize(r *http.Request, t project.DBToolTarget) error {
	principal, _ := auth.PrincipalFrom(r.Context())
	ids, err := p.projects.DBToolProjects(r.Context(), t)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if principal.ScopeFor(id).Covers(auth.ScopeOperate) {
			return nil
		}
	}
	return errDBToolTarget
}

// adminerDrivers are the driver ids Adminer 5 reads from the query, in the order it looks
// for them; the first one present is the driver and its value the server. Without any,
// the driver is "server" (MySQL) with an empty server.
var adminerDrivers = []string{"sqlite", "pgsql", "oracle", "mssql", "server"}

// loginBodyLimit caps the form body read for the login check. Adminer's login form is
// tiny; larger bodies (SQL, imports) are not read, see adminerTargets.
const loginBodyLimit = 64 << 10

// adminerTargets reads what an Adminer request opens. target comes from the query: Adminer
// connects only to the driver, server and username named there, on every page. login
// comes from a login form post (auth[driver], auth[server], auth[username]); Adminer only
// stores it and redirects to the query of that login, which is checked in turn, so it is
// refused early rather than relied on. Both are nil for requests that open nothing (the
// login page, static files). Query shapes PHP would read differently from Go (repeated or
// array parameters, a second driver) are refused: the plugin connects only when its own
// reading matches the target the proxy checked, and refusing here gives a clear answer.
func adminerTargets(r *http.Request) (target, login *project.DBToolTarget, err error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, nil, errDBToolQuery
	}
	target, err = queryTarget(q)
	if err != nil {
		return nil, nil, err
	}
	if r.Method != http.MethodPost {
		return target, nil, nil
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/x-www-form-urlencoded" {
		return target, nil, nil
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, loginBodyLimit+1))
	if err != nil {
		return nil, nil, err
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if len(head) > loginBodyLimit {
		return target, nil, nil
	}
	form, err := url.ParseQuery(string(head))
	if err != nil {
		return nil, nil, errDBToolQuery
	}
	login, err = loginTarget(form)
	return target, login, err
}

var errDBToolQuery = fmt.Errorf("%w: the database browser request names its database in an unexpected way", auth.ErrForbidden)

// phpName is the variable a parameter name becomes in PHP: cut at a NUL byte, leading
// spaces dropped, and an array suffix ("server[x]") reduced to its base.
func phpName(key string) string {
	if i := strings.IndexByte(key, 0); i >= 0 {
		key = key[:i]
	}
	key = strings.TrimLeft(key, " ")
	if i := strings.IndexByte(key, '['); i >= 0 {
		key = key[:i]
	}
	return key
}

// single returns the one value PHP would see for the variable name, refusing every other
// spelling of it and repeated parameters with different values.
func single(q url.Values, name string) (string, bool, error) {
	var val string
	found := false
	for key, vals := range q {
		if phpName(key) != name {
			continue
		}
		if key != name {
			return "", false, errDBToolQuery
		}
		for _, v := range vals {
			if found && v != val {
				return "", false, errDBToolQuery
			}
			val, found = v, true
		}
	}
	return val, found, nil
}

func queryTarget(q url.Values) (*project.DBToolTarget, error) {
	t := project.DBToolTarget{Driver: "server"}
	drivers := 0
	for _, d := range adminerDrivers {
		v, ok, err := single(q, d)
		if err != nil {
			return nil, err
		}
		if ok {
			drivers++
			t.Driver, t.Server = d, v
		}
	}
	user, hasUser, err := single(q, "username")
	if err != nil {
		return nil, err
	}
	if drivers > 1 {
		return nil, errDBToolQuery
	}
	if drivers == 0 && !hasUser {
		return nil, nil
	}
	t.Username = user
	return &t, nil
}

// loginTarget reads a login form post. Any auth[...] field makes it one.
func loginTarget(form url.Values) (*project.DBToolTarget, error) {
	fields := map[string]string{}
	for key, vals := range form {
		if phpName(key) != "auth" {
			continue
		}
		field, ok := strings.CutPrefix(key, "auth[")
		field, ok2 := strings.CutSuffix(field, "]")
		if !ok || !ok2 || strings.ContainsAny(field, "[]") {
			return nil, errDBToolQuery
		}
		for _, v := range vals {
			if prev, seen := fields[field]; seen && prev != v {
				return nil, errDBToolQuery
			}
			fields[field] = v
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return &project.DBToolTarget{Driver: fields["driver"], Server: fields["server"], Username: fields["username"]}, nil
}

func (p *dbToolProxy) proxyFor(dial string) *httputil.ReverseProxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rp, ok := p.proxies[dial]; ok {
		return rp
	}
	upstream := &url.URL{Scheme: "http", Host: dial}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			path := strings.TrimPrefix(pr.In.URL.Path, project.DBToolPathPrefix)
			if path == "" {
				path = "/"
			}
			pr.Out.URL.Path, pr.Out.URL.RawPath = path, ""
			pr.SetXForwarded()
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		ModifyResponse: func(res *http.Response) error {
			if loc := res.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, project.DBToolPathPrefix+"/") {
				res.Header.Set("Location", project.DBToolPathPrefix+loc)
			}
			return nil
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          20,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute, // long exports and imports
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "The database browser did not answer: "+err.Error(), http.StatusBadGateway)
		},
	}
	p.proxies[dial] = rp
	return rp
}

package api

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

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
type dbToolProxy struct {
	projects *project.Manager
	mu       sync.Mutex
	proxies  map[string]*httputil.ReverseProxy
}

func newDBToolProxy(projects *project.Manager) *dbToolProxy {
	return &dbToolProxy{projects: projects, proxies: map[string]*httputil.ReverseProxy{}}
}

func (p *dbToolProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dial, err := p.projects.DBToolDial(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if dial == "" {
		http.Error(w, "The database browser is not running. Open a database from a project's Database tab to start it.", http.StatusServiceUnavailable)
		return
	}
	p.proxyFor(dial).ServeHTTP(w, r)
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

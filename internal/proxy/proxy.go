// Package proxy is the embedded reverse proxy that routes browser requests by host name to
// project web servers (and to Staqio's own UI), with TLS from the local CA.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/seramos/staqio/internal/tlsca"
)

// Target is where a host name is routed to.
type Target struct {
	ProjectID   string
	ProjectName string
	Slug        string
	// Dial is the upstream address (host:port) reachable from the Staqio process.
	Dial string
	// Running is false when the project's web container is not up.
	Running bool
}

// Table is a snapshot of the routing state.
type Table struct {
	// Routes maps lower-case host names to project targets.
	Routes map[string]Target
	// UIHosts are host names served by Staqio's own UI (e.g. staqio.test, the public host).
	UIHosts map[string]bool
	// ForceHTTPS redirects plain HTTP requests for known hosts to HTTPS.
	ForceHTTPS bool
	// HTTPSPort is the host-side HTTPS port used in redirects (0 = 443).
	HTTPSPort int
	// StaqioURL is where the UI can be reached (for links on error pages).
	StaqioURL string
}

// Source produces the current routing table.
type Source func(ctx context.Context) (Table, error)

// Router resolves hosts to targets with a short cache in front of the source.
type Router struct {
	source Source
	ttl    time.Duration
	log    *slog.Logger

	mu      sync.Mutex
	table   Table
	fetched time.Time
	fetchMu sync.Mutex
}

// NewRouter creates a router.
func NewRouter(source Source, ttl time.Duration, log *slog.Logger) *Router {
	return &Router{source: source, ttl: ttl, log: log}
}

// Invalidate drops the cached table so the next request re-reads the source.
func (r *Router) Invalidate() {
	r.mu.Lock()
	r.fetched = time.Time{}
	r.mu.Unlock()
}

// Table returns the current table, refreshing it when stale.
func (r *Router) Table(ctx context.Context) Table {
	r.mu.Lock()
	if time.Since(r.fetched) < r.ttl {
		t := r.table
		r.mu.Unlock()
		return t
	}
	r.mu.Unlock()

	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	r.mu.Lock()
	if time.Since(r.fetched) < r.ttl {
		t := r.table
		r.mu.Unlock()
		return t
	}
	r.mu.Unlock()
	t, err := r.source(ctx)
	if err != nil {
		r.log.Warn("proxy routing table refresh failed", "err", err)
		r.mu.Lock()
		t = r.table
		r.mu.Unlock()
		return t
	}
	r.mu.Lock()
	r.table, r.fetched = t, time.Now()
	r.mu.Unlock()
	return t
}

// Handler serves proxied requests. ui handles requests for Staqio's own host names.
type Handler struct {
	router *Router
	ui     http.Handler
	log    *slog.Logger
	tls    bool // whether an HTTPS listener exists (for redirects)

	mu      sync.Mutex
	proxies map[string]*httputil.ReverseProxy
}

// NewHandler creates the proxy handler.
func NewHandler(router *Router, ui http.Handler, tlsEnabled bool, log *slog.Logger) *Handler {
	return &Handler{router: router, ui: ui, log: log, tls: tlsEnabled, proxies: map[string]*httputil.ReverseProxy{}}
}

// hostOf extracts the lower-case host name without port.
func hostOf(r *http.Request) string {
	h := r.Host
	if strings.HasPrefix(h, "[") {
		if i := strings.LastIndex(h, "]"); i > 0 {
			return strings.ToLower(h[1:i])
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// ServeHTTP routes by host.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOf(r)
	table := h.router.Table(r.Context())

	// Staqio's own UI: its host names, bare IPs and empty hosts.
	if host == "" || table.UIHosts[host] || net.ParseIP(host) != nil {
		if h.tls && table.ForceHTTPS && r.TLS == nil && net.ParseIP(host) == nil {
			redirectHTTPS(w, r, table.HTTPSPort)
			return
		}
		h.ui.ServeHTTP(w, r)
		return
	}
	target, ok := table.Routes[host]
	if !ok {
		h.errorPage(w, http.StatusNotFound, "Unknown host", fmt.Sprintf("No Staqio project is configured for <strong>%s</strong>.", html.EscapeString(host)), table.StaqioURL)
		return
	}
	if h.tls && table.ForceHTTPS && r.TLS == nil {
		redirectHTTPS(w, r, table.HTTPSPort)
		return
	}
	if !target.Running || target.Dial == "" {
		h.errorPage(w, http.StatusServiceUnavailable, target.ProjectName+" is stopped",
			fmt.Sprintf("The project <strong>%s</strong> is not running. Start it in Staqio.", html.EscapeString(target.ProjectName)), table.StaqioURL)
		return
	}
	h.proxyFor(target.Dial).ServeHTTP(w, r)
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request, port int) {
	host := hostOf(r)
	if port != 0 && port != 443 {
		host = net.JoinHostPort(host, fmt.Sprint(port))
	}
	u := url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
}

func (h *Handler) proxyFor(dial string) *httputil.ReverseProxy {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.proxies[dial]; ok {
		return p
	}
	upstream := &url.URL{Scheme: "http", Host: dial}
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			// Keep the original Host so the application sees its own domain.
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute, // long-running PHP requests
			ForceAttemptHTTP2:     false,
		},
		FlushInterval: 100 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			h.log.Debug("proxy upstream error", "host", r.Host, "upstream", dial, "err", err)
			h.errorPage(w, http.StatusBadGateway, "Upstream unavailable", "The project's web server did not respond. It may still be starting.", "")
		},
	}
	h.proxies[dial] = p
	return p
}

func (h *Handler) errorPage(w http.ResponseWriter, status int, title, message, staqioURL string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	link := ""
	if staqioURL != "" {
		link = fmt.Sprintf(`<p><a href="%s">Open Staqio</a></p>`, html.EscapeString(staqioURL))
	}
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>%s · Staqio</title>
<style>body{font-family:system-ui,sans-serif;background:#0f1115;color:#e6e8ee;margin:0;display:grid;place-items:center;min-height:100vh}
main{max-width:34rem;padding:2rem}h1{font-weight:600;margin:0 0 .5rem}p{color:#9aa1ae}a{color:#a5b4fc}strong{color:#e6e8ee}
.code{font-family:ui-monospace,monospace;color:#6b7280;font-size:.85rem}</style></head>
<body><main><p class="code">%d · Staqio proxy</p><h1>%s</h1><p>%s</p>%s</main></body></html>`,
		html.EscapeString(title), status, html.EscapeString(title), message, link)
}

// Server runs the HTTP and optional HTTPS listeners of the proxy.
type Server struct {
	handler   *Handler
	router    *Router
	certs     *tlsca.Store
	httpAddr  string
	httpsAddr string
	log       *slog.Logger
	// PublicHosts are extra names the TLS listener issues certificates for (UI hosts).
	extraAllow func(host string) bool
}

// NewServer creates the proxy server. httpsAddr may be empty to disable TLS.
func NewServer(handler *Handler, router *Router, certs *tlsca.Store, httpAddr, httpsAddr string, extraAllow func(string) bool, log *slog.Logger) *Server {
	return &Server{handler: handler, router: router, certs: certs, httpAddr: httpAddr, httpsAddr: httpsAddr, extraAllow: extraAllow, log: log}
}

// allowHost decides whether TLS certificates may be issued for a name: known routes, UI
// hosts, IP addresses and operator-configured names.
func (s *Server) allowHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	t := s.router.Table(context.Background())
	if t.UIHosts[host] {
		return true
	}
	if _, ok := t.Routes[host]; ok {
		return true
	}
	return s.extraAllow != nil && s.extraAllow(host)
}

// Run serves until ctx is cancelled. Listener failures (e.g. port in use) are logged and
// reported through the returned error only when both listeners fail to start.
func (s *Server) Run(ctx context.Context) error {
	var servers []*http.Server
	started := 0
	start := func(addr string, tlsCfg func() *http.Server) {
		if addr == "" {
			return
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.log.Warn("proxy listener unavailable", "addr", addr, "err", err)
			return
		}
		srv := tlsCfg()
		servers = append(servers, srv)
		started++
		go func() {
			var err error
			if srv.TLSConfig != nil {
				err = srv.ServeTLS(ln, "", "")
			} else {
				err = srv.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.log.Error("proxy listener stopped", "addr", addr, "err", err)
			}
		}()
		s.log.Info("proxy listening", "addr", addr, "tls", srv.TLSConfig != nil)
	}
	base := func() *http.Server {
		return &http.Server{Handler: s.handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	}
	start(s.httpAddr, base)
	if s.certs != nil {
		start(s.httpsAddr, func() *http.Server {
			srv := base()
			srv.TLSConfig = s.certs.TLSConfig(s.allowHost)
			return srv
		})
	}
	if started == 0 {
		return errors.New("proxy: no listener could be started")
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	return nil
}

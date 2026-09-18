// Package server assembles the HTTP server: middleware, API routes and the embedded SPA.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/api"
	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
)

// Options configure the server.
type Options struct {
	Addr string
	// AllowedOrigins are additional browser origins accepted for state-changing requests
	// (e.g. the Vite dev server). Same-origin requests are always accepted.
	AllowedOrigins []string
	Log            *slog.Logger
	// MCP is mounted at /mcp when set. It authenticates with bearer tokens itself and
	// is outside the cookie/CSRF scheme of /api/.
	MCP http.Handler
}

// Server wraps http.Server.
type Server struct {
	http *http.Server
	log  *slog.Logger
}

// New builds the handler chain. dist is the embedded frontend (may be nil).
func New(opts Options, a *api.API, sessions *auth.Service, dist fs.FS) *Server {
	mux := http.NewServeMux()
	protect := func(next http.Handler) http.Handler {
		return sessions.Middleware(http.HandlerFunc(unauthorizedJSON))(next)
	}
	a.Mount(mux, protect)
	if opts.MCP != nil {
		mux.Handle("/mcp", opts.MCP)
	}
	mux.Handle("/", spaHandler(dist))

	var handler http.Handler = mux
	handler = csrfMiddleware(opts.AllowedOrigins)(handler)
	handler = securityHeaders(handler)
	handler = requestContext(handler)
	handler = logging(opts.Log)(handler)
	handler = recoverer(opts.Log)(handler)

	return &Server{
		http: &http.Server{
			Addr:              opts.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      0, // long-running operations (image pulls) are bounded by request contexts
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
		},
		log: opts.Log,
	}
}

// Handler returns the fully wrapped HTTP handler (used by tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe runs until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.http.Addr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

func unauthorizedJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"authentication required"}}`))
}

// ---- middleware --------------------------------------------------------------

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", rec)
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"an internal error occurred"}}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				level := slog.LevelInfo
				if sw.status >= 500 {
					level = slog.LevelError
				} else if r.Method == http.MethodGet {
					level = slog.LevelDebug
				}
				log.Log(r.Context(), level, "http",
					"method", r.Method, "path", r.URL.Path, "status", sw.status,
					"bytes", sw.bytes, "duration", time.Since(start).String(),
					"ip", auth.ClientIP(r), "request_id", w.Header().Get("X-Request-Id"))
			}
		})
	}
}

func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		ctx := audit.WithClientIP(r.Context(), auth.ClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// csrfMiddleware protects state-changing API requests. Defence in depth on top of the
// SameSite=Lax session cookie:
//   - the custom X-Requested-With header must be present (cannot be set cross-site without CORS),
//   - if the browser sends Origin / Sec-Fetch-Site they must indicate same-origin (or an
//     explicitly allowed origin such as the dev server).
func csrfMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[strings.ToLower(strings.TrimSuffix(o, "/"))] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			originOK := origin == "" || sameOrigin(origin, r) || allowed[strings.ToLower(origin)]

			// CORS preflight for the dev server only.
			if r.Method == http.MethodOptions && origin != "" {
				if allowed[strings.ToLower(origin)] {
					setCORS(w, origin)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if origin != "" && allowed[strings.ToLower(origin)] {
				setCORS(w, origin)
			}

			switch r.Method {
			case http.MethodGet, http.MethodHead:
				next.ServeHTTP(w, r)
				return
			}
			if !originOK {
				forbidden(w, "cross-origin request rejected")
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				if origin == "" || !allowed[strings.ToLower(origin)] {
					forbidden(w, "cross-site request rejected")
					return
				}
			}
			if r.Header.Get("X-Requested-With") != "Envoryx" {
				forbidden(w, "missing X-Requested-With header")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setCORS(w http.ResponseWriter, origin string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Add("Vary", "Origin")
}

func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func forbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"` + msg + `"}}`))
}

// ---- SPA ------------------------------------------------------------------------

func spaHandler(dist fs.FS) http.Handler {
	if dist == nil {
		return http.HandlerFunc(notBuilt)
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return http.HandlerFunc(notBuilt)
	}
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := fs.Stat(dist, p); err == nil && !f.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Client-side route: serve the app shell.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

func notBuilt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><title>Envoryx</title><body style="font-family:system-ui;padding:2rem"><h1>Envoryx</h1><p>The web frontend has not been built. Run <code>make web</code> or use the official Docker image.</p><p>The API is available under <code>/api/v1</code>.</p></body>`))
}

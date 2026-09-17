package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/config"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/hostpath"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/stats"
	"github.com/seramos/staqio/internal/store"
)

// Deps are the services the API handlers use.
type Deps struct {
	Config   config.Config
	Version  string
	Store    *store.Store
	Auth     *auth.Service
	Audit    *audit.Logger
	Engine   docker.Engine
	Projects *project.Manager
	Catalog  *runtime.Catalog
	Stats    *stats.Collector
	HostPath *hostpath.Resolver
	Log      *slog.Logger
	// StartedAt is used for uptime reporting.
	StartedAt time.Time
}

// API holds handlers.
type API struct {
	d Deps
}

// New creates the API.
func New(d Deps) *API { return &API{d: d} }

// Mount registers all routes on mux. protect wraps handlers that require a session.
func (a *API) Mount(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	// Public endpoints.
	mux.HandleFunc("GET /api/v1/health", a.health)
	mux.HandleFunc("GET /api/v1/setup", a.setupStatus)
	mux.HandleFunc("POST /api/v1/setup", a.setup)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)

	p := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, protect(h))
	}
	p("GET /api/v1/auth/me", a.me)
	p("POST /api/v1/auth/password", a.changePassword)

	p("GET /api/v1/dashboard", a.dashboard)
	p("GET /api/v1/runtimes", a.runtimes)
	p("GET /api/v1/docker", a.dockerOverview)
	p("GET /api/v1/settings", a.settings)
	p("GET /api/v1/audit", a.auditLog)
	p("GET /api/v1/system/reconcile", a.reconcileReport)
	p("POST /api/v1/system/reconcile", a.reconcileNow)

	p("GET /api/v1/projects", a.listProjects)
	p("POST /api/v1/projects", a.createProject)
	p("POST /api/v1/projects/preview", a.previewProject)
	p("GET /api/v1/projects/{id}", a.getProject)
	p("PATCH /api/v1/projects/{id}", a.updateProject)
	p("DELETE /api/v1/projects/{id}", a.deleteProject)
	p("POST /api/v1/projects/{id}/start", a.startProject)
	p("POST /api/v1/projects/{id}/stop", a.stopProject)
	p("POST /api/v1/projects/{id}/restart", a.restartProject)
	p("GET /api/v1/projects/{id}/services", a.projectServices)
	p("GET /api/v1/projects/{id}/plan", a.projectPlan)
	p("GET /api/v1/projects/{id}/stats", a.projectStats)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, newError(http.StatusNotFound, "not_found", "unknown API route"))
	})
}

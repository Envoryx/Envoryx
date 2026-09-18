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
	"github.com/seramos/staqio/internal/tlsca"
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
	Certs    *tlsca.Store
	Proxy    *ProxyInfo
	// MCP is the MCP endpoint handler (nil = disabled); mounted at /mcp by the server.
	MCP http.Handler
	Log *slog.Logger
	// StartedAt is used for uptime reporting.
	StartedAt time.Time
	// AllowedOriginHosts are extra origins (host[:port]) permitted for WebSocket upgrades,
	// e.g. the Vite dev server. Same-origin is always allowed.
	AllowedOriginHosts []string
}

// ProxyInfo describes the embedded proxy for the UI. HTTPPort/HTTPSPort are the host-side
// ports (0 = not published / unknown).
type ProxyInfo struct {
	Enabled   bool
	HTTPPort  int
	HTTPSPort int
	// InDocker is false on bare metal (proxy dials published ports instead).
	InDocker bool
	// Address is the container's own IP when it is reachable directly (macvlan/ipvlan)
	// instead of through published host ports; DNS entries must point at it.
	Address string
	// Invalidate refreshes the routing table after changes.
	Invalidate func()
}

// API holds handlers.
type API struct {
	d Deps
}

// New creates the API.
func New(d Deps) *API { return &API{d: d} }

// SetAllowedOriginHosts configures extra WebSocket origins (dev server).
func (a *API) SetAllowedOriginHosts(hosts []string) { a.d.AllowedOriginHosts = hosts }

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
	p("GET /api/v1/docker/images/unused", a.unusedImages)
	p("POST /api/v1/docker/images/prune", a.pruneImages)
	p("GET /api/v1/settings", a.settings)
	p("PATCH /api/v1/settings", a.updateSettings)
	p("GET /api/v1/tokens", a.listTokens)
	p("POST /api/v1/tokens", a.createToken)
	p("DELETE /api/v1/tokens/{id}", a.deleteToken)
	p("GET /api/v1/settings/tls", a.tlsInfo)
	p("GET /api/v1/settings/tls/ca.crt", a.downloadCA)
	p("PUT /api/v1/settings/tls/custom", a.setCustomCert)
	p("DELETE /api/v1/settings/tls/custom", a.clearCustomCert)
	p("GET /api/v1/projects/{id}/domains", a.listDomains)
	p("POST /api/v1/projects/{id}/domains", a.addDomain)
	p("DELETE /api/v1/projects/{id}/domains/{domain}", a.removeDomain)
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
	p("GET /api/v1/projects/{id}/services/{kind}/logs", a.serviceLogs)
	p("GET /api/v1/projects/{id}/services/{kind}/logs/ws", a.serviceLogsWS)
	p("GET /api/v1/projects/{id}/services/{kind}/terminal/ws", a.terminalWS)
	p("GET /api/v1/projects/{id}/git", a.gitStatus)
	p("PUT /api/v1/projects/{id}/git", a.gitSet)
	p("POST /api/v1/projects/{id}/git/clone", a.gitClone)
	p("POST /api/v1/projects/{id}/git/pull", a.gitPull)
	p("POST /api/v1/projects/{id}/git/checkout", a.gitCheckout)
	p("GET /api/v1/settings/deploy-key", a.deployKey)
	p("POST /api/v1/settings/deploy-key/regenerate", a.deployKeyRegenerate)
	p("GET /api/v1/projects/{id}/actions", a.listActions)
	p("GET /api/v1/projects/{id}/actions/{action}/ws", a.actionWS)
	p("GET /api/v1/projects/{id}/backups", a.listBackups)
	p("POST /api/v1/projects/{id}/backups", a.createBackup)
	p("DELETE /api/v1/projects/{id}/backups/{backup}", a.deleteBackup)
	p("POST /api/v1/projects/{id}/backups/{backup}/restore", a.restoreBackup)
	p("GET /api/v1/projects/{id}/backups/{backup}/download", a.downloadBackup)
	p("GET /api/v1/projects/{id}/extras", a.extraServices)
	p("GET /api/v1/projects/{id}/database", a.databaseInfo)
	p("GET /api/v1/projects/{id}/database/credentials", a.databaseCredentials)
	p("POST /api/v1/projects/{id}/database/rotate", a.databaseRotate)
	p("POST /api/v1/projects/{id}/database/expose", a.databaseExpose)
	p("GET /api/v1/projects/{id}/database/databases", a.databaseList)
	p("POST /api/v1/projects/{id}/database/databases", a.databaseCreate)
	p("DELETE /api/v1/projects/{id}/database/databases/{name}", a.databaseDrop)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, newError(http.StatusNotFound, "not_found", "unknown API route"))
	})
}

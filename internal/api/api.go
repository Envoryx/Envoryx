package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/acme"
	"github.com/envoryx/envoryx/internal/notify"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/hostpath"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/stats"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/tlsca"
	"github.com/envoryx/envoryx/internal/update"
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
	ACME     *acme.Manager
	Notify   *notify.Service
	// Updates reports whether a newer release exists (never nil).
	Updates *update.Checker
	Proxy   *ProxyInfo
	// Instance manages backups of the instance itself (nil = disabled).
	Instance *instance.Store
	// DB is the live database, used for instance backups.
	DB *sql.DB
	// Restart asks the server to shut down and start again (e.g. to apply a restore).
	Restart func()
	// Warnings are startup findings worth showing in the UI (storage checks).
	Warnings []string
	// MCP is the MCP endpoint handler (nil = disabled); mounted at /mcp by the server.
	MCP http.Handler
	// SSH describes the embedded SSH server (nil = disabled).
	SSH *SSHInfo
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

// SSHInfo describes the embedded SSH server for the UI.
type SSHInfo struct {
	Enabled bool `json:"enabled"`
	// Port is the host-side port (0 = not published).
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
}

// API holds handlers.
type API struct {
	d Deps
}

// New creates the API.
func New(d Deps) *API { return &API{d: d} }

// SetAllowedOriginHosts configures extra WebSocket origins (dev server).
func (a *API) SetAllowedOriginHosts(hosts []string) { a.d.AllowedOriginHosts = hosts }

// instanceRoutesForConfinedTokens are the routes without a project id that a token
// limited to particular projects may still call; lists filter by the token's projects.
var instanceRoutesForConfinedTokens = map[string]bool{
	"GET /api/v1/auth/me":  true,
	"GET /api/v1/runtimes": true,
	"GET /api/v1/projects": true,
	// Filtered to the token's projects like the list.
	"GET /api/v1/operations": true,
}

// guard enforces a token's scope and project restriction for one route. Sessions pass.
// Project routes must name one of the token's projects; other routes are instance-wide
// and closed to confined tokens except for the few listed above.
func (a *API) guard(need auth.Scope, pattern string, h http.HandlerFunc) http.HandlerFunc {
	perProject := strings.Contains(pattern, "/projects/{id}")
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.TokenName != "" {
			var err error
			switch {
			case perProject:
				err = p.Require(need, r.PathValue("id"))
			case p.Restricted() && !instanceRoutesForConfinedTokens[pattern]:
				err = fmt.Errorf("%w: this token is limited to particular projects", auth.ErrForbidden)
			default:
				err = p.Require(need, "")
			}
			if err != nil {
				writeError(w, r, err)
				return
			}
		}
		h(w, r)
	}
}

// Mount registers all routes on mux. protect wraps handlers that require a session.
func (a *API) Mount(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	// Public endpoints.
	mux.HandleFunc("GET /api/v1/health", a.health)
	mux.HandleFunc("GET /api/v1/setup", a.setupStatus)
	mux.HandleFunc("POST /api/v1/setup", a.setup)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)

	// Every protected route declares the token scope it needs: rd (read), op (operate)
	// or adm (admin). Browser sessions pass all of them; see guard for the project
	// restriction of confined tokens.
	route := func(need auth.Scope, pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, protect(a.guard(need, pattern, h)))
	}
	rd := func(pattern string, h http.HandlerFunc) { route(auth.ScopeRead, pattern, h) }
	op := func(pattern string, h http.HandlerFunc) { route(auth.ScopeOperate, pattern, h) }
	adm := func(pattern string, h http.HandlerFunc) { route(auth.ScopeAdmin, pattern, h) }
	rd("GET /api/v1/auth/me", a.me)
	adm("POST /api/v1/auth/password", a.changePassword)

	adm("GET /api/v1/dashboard", a.dashboard)
	rd("GET /api/v1/runtimes", a.runtimes)
	adm("GET /api/v1/docker", a.dockerOverview)
	adm("GET /api/v1/docker/images/unused", a.unusedImages)
	adm("POST /api/v1/docker/images/prune", a.pruneImages)
	adm("GET /api/v1/settings", a.settings)
	adm("PATCH /api/v1/settings", a.updateSettings)
	adm("GET /api/v1/tokens", a.listTokens)
	adm("POST /api/v1/tokens", a.createToken)
	adm("DELETE /api/v1/tokens/{id}", a.deleteToken)
	adm("GET /api/v1/settings/tls", a.tlsInfo)
	adm("GET /api/v1/settings/notifications", a.notificationStatus)
	adm("PUT /api/v1/settings/notifications", a.setNotifications)
	adm("POST /api/v1/settings/notifications/test", a.testNotifications)
	adm("GET /api/v1/settings/tls/acme", a.acmeStatus)
	adm("PUT /api/v1/settings/tls/acme", a.setACME)
	adm("DELETE /api/v1/settings/tls/acme", a.clearACME)
	adm("POST /api/v1/settings/tls/acme/issue", a.issueACME)
	adm("GET /api/v1/settings/tls/ca.crt", a.downloadCA)
	adm("PUT /api/v1/settings/tls/custom", a.setCustomCert)
	adm("DELETE /api/v1/settings/tls/custom", a.clearCustomCert)
	op("POST /api/v1/projects/{id}/ide/stop-backend", a.stopIDEBackend)
	rd("GET /api/v1/projects/{id}/workers", a.listWorkers)
	adm("POST /api/v1/projects/{id}/workers", a.addWorker)
	adm("PUT /api/v1/projects/{id}/workers/{worker}", a.updateWorker)
	adm("DELETE /api/v1/projects/{id}/workers/{worker}", a.removeWorker)
	rd("GET /api/v1/projects/{id}/domains", a.listDomains)
	op("POST /api/v1/projects/{id}/domains", a.addDomain)
	op("DELETE /api/v1/projects/{id}/domains/{domain}", a.removeDomain)
	adm("GET /api/v1/audit", a.auditLog)
	adm("GET /api/v1/instance/backups", a.listInstanceBackups)
	adm("POST /api/v1/instance/backups", a.createInstanceBackup)
	adm("POST /api/v1/instance/backups/upload", a.uploadInstanceBackup)
	adm("DELETE /api/v1/instance/backups/{id}", a.deleteInstanceBackup)
	adm("GET /api/v1/instance/backups/{id}/download", a.downloadInstanceBackup)
	adm("POST /api/v1/instance/backups/{id}/restore", a.restoreInstanceBackup)
	adm("DELETE /api/v1/instance/restore", a.cancelInstanceRestore)
	adm("GET /api/v1/system/reconcile", a.reconcileReport)
	adm("GET /api/v1/system/diagnostics", a.diagnostics)
	adm("POST /api/v1/system/reconcile", a.reconcileNow)

	rd("GET /api/v1/projects", a.listProjects)
	rd("GET /api/v1/operations", a.listOperations)
	adm("POST /api/v1/projects", a.createProject)
	adm("POST /api/v1/projects/preview", a.previewProject)
	rd("GET /api/v1/projects/{id}", a.getProject)
	adm("PATCH /api/v1/projects/{id}", a.updateProject)
	adm("DELETE /api/v1/projects/{id}", a.deleteProject)
	op("POST /api/v1/projects/{id}/start", a.startProject)
	op("POST /api/v1/projects/{id}/stop", a.stopProject)
	op("POST /api/v1/projects/{id}/restart", a.restartProject)
	op("POST /api/v1/projects/{id}/images", a.useImage)
	op("GET /api/v1/dbtool", a.dbToolStatus)
	adm("PUT /api/v1/dbtool", a.setDBTool)
	op("POST /api/v1/projects/{id}/dbtool", a.openDBTool)
	// The database browser lives inside the UI origin so the session protects it.
	mux.Handle(project.DBToolPathPrefix+"/", protect(a.guard(auth.ScopeOperate, project.DBToolPathPrefix+"/", newDBToolProxy(a.d.Projects).ServeHTTP)))
	rd("GET /api/v1/projects/{id}/services", a.projectServices)
	rd("GET /api/v1/projects/{id}/plan", a.projectPlan)
	rd("GET /api/v1/projects/{id}/stats", a.projectStats)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs", a.serviceLogs)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs/ws", a.serviceLogsWS)
	op("GET /api/v1/projects/{id}/services/{kind}/terminal/ws", a.terminalWS)
	rd("GET /api/v1/projects/{id}/git", a.gitStatus)
	op("PUT /api/v1/projects/{id}/git", a.gitSet)
	op("POST /api/v1/projects/{id}/git/clone", a.gitClone)
	op("POST /api/v1/projects/{id}/git/pull", a.gitPull)
	op("POST /api/v1/projects/{id}/git/checkout", a.gitCheckout)
	adm("GET /api/v1/settings/deploy-key", a.deployKey)
	adm("POST /api/v1/settings/deploy-key/regenerate", a.deployKeyRegenerate)
	rd("GET /api/v1/projects/{id}/actions", a.listActions)
	op("GET /api/v1/projects/{id}/actions/{action}/ws", a.actionWS)
	rd("GET /api/v1/projects/{id}/backups", a.listBackups)
	adm("PUT /api/v1/projects/{id}/backups/schedule", a.setBackupSchedule)
	op("POST /api/v1/projects/{id}/backups", a.createBackup)
	adm("DELETE /api/v1/projects/{id}/backups/{backup}", a.deleteBackup)
	adm("POST /api/v1/projects/{id}/backups/{backup}/restore", a.restoreBackup)
	op("GET /api/v1/projects/{id}/backups/{backup}/download", a.downloadBackup)
	rd("GET /api/v1/projects/{id}/extras", a.extraServices)
	rd("GET /api/v1/projects/{id}/storage", a.storageInfo)
	op("GET /api/v1/projects/{id}/storage/credentials", a.storageCredentials)
	adm("PUT /api/v1/projects/{id}/storage/public", a.storagePublic)
	rd("GET /api/v1/projects/{id}/database", a.databaseInfo)
	op("GET /api/v1/projects/{id}/database/credentials", a.databaseCredentials)
	adm("POST /api/v1/projects/{id}/database/rotate", a.databaseRotate)
	adm("POST /api/v1/projects/{id}/database/expose", a.databaseExpose)
	rd("GET /api/v1/projects/{id}/database/databases", a.databaseList)
	op("POST /api/v1/projects/{id}/database/databases", a.databaseCreate)
	adm("DELETE /api/v1/projects/{id}/database/databases/{name}", a.databaseDrop)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, newError(http.StatusNotFound, "not_found", "unknown API route"))
	})
}

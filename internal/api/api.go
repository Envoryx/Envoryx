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
	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/oidc"
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
	// Instance manages backups of the instance itself; nil disables them.
	Instance *instance.Store
	// Offsite copies backups to S3, SFTP or WebDAV targets; nil disables it.
	Offsite *offsite.Syncer
	// DB is the live database, used for instance backups.
	DB *sql.DB
	// Restart asks the server to shut down and start again (e.g. to apply a restore).
	Restart func()
	// Warnings are startup findings worth showing in the UI (storage checks).
	Warnings []string
	// MCP is the MCP endpoint handler, mounted at /mcp by the server; nil disables it.
	MCP http.Handler
	// OIDC signs users in through an OpenID Connect provider; nil disables it.
	OIDC *oidc.Service
	// SSH describes the embedded SSH server; nil when it's disabled.
	SSH *SSHInfo
	Log *slog.Logger
	// StartedAt is used for uptime reporting.
	StartedAt time.Time
	// AllowedOriginHosts are extra origins (host[:port]) permitted for WebSocket upgrades,
	// e.g. the Vite dev server. Same-origin is always allowed.
	AllowedOriginHosts []string
}

// ProxyInfo describes the embedded proxy for the UI. HTTPPort/HTTPSPort are the host-side
// ports, 0 when not published or unknown.
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
	// Port is the host-side port, 0 if not published.
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
	// FingerprintMD5 is the legacy form JetBrains IDEs show when they ask to trust the key.
	FingerprintMD5 string `json:"fingerprintMd5"`
}

// API holds handlers.
type API struct {
	d Deps
}

// New creates the API.
func New(d Deps) *API { return &API{d: d} }

// SetAllowedOriginHosts configures extra WebSocket origins (dev server).
func (a *API) SetAllowedOriginHosts(hosts []string) { a.d.AllowedOriginHosts = hosts }

// instanceRoutesForConfined are the routes without a project id that a principal
// confined to particular projects (a token with a project list, a user whose global role
// is none) may still call; lists filter by its projects. All of them only read.
var instanceRoutesForConfined = map[string]bool{
	"GET /api/v1/auth/me":  true,
	"GET /api/v1/runtimes": true,
	"GET /api/v1/projects": true,
	// Filtered to the principal's projects like the list.
	"GET /api/v1/operations": true,
	// Computes the next times of a schedule the caller sends; it touches no data. The cron
	// editor of a project needs it.
	"POST /api/v1/cron/preview": true,
}

// instanceRoutesForConfinedUsers are the further instance routes a confined user's
// browser session needs to use the UI: the dashboard (filtered), the settings the
// project pages read (trimmed for non-admins), and the own password and tokens.
var instanceRoutesForConfinedUsers = map[string]bool{
	"GET /api/v1/dashboard":        true,
	"GET /api/v1/settings":         true,
	"POST /api/v1/auth/password":   true,
	"GET /api/v1/auth/ssh-keys":    true,
	"PUT /api/v1/auth/ssh-keys":    true,
	"GET /api/v1/tokens":           true,
	"POST /api/v1/tokens":          true,
	"DELETE /api/v1/tokens/{id}":   true,
	"GET /api/v1/metrics/overview": true,
}

// routesForAnyProject serve every project alike: the database browser's status and the
// browser itself. Holding the level in any project opens the door; the browser proxy then
// checks each request's database against the principal's own projects (dbToolProxy).
var routesForAnyProject = map[string]bool{
	"GET /api/v1/dbtool":           true,
	project.DBToolPathPrefix + "/": true,
}

// guard enforces the principal's access for one route: the user's role (in the project
// for project routes) and, for an API token, its scope and project list as well.
// Instance-wide routes are closed to confined principals except for the few listed above.
func (a *API) guard(need auth.Scope, pattern string, h http.HandlerFunc) http.HandlerFunc {
	perProject := strings.Contains(pattern, "/projects/{id}")
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		var err error
		switch {
		case perProject:
			err = p.Require(need, r.PathValue("id"))
		case routesForAnyProject[pattern]:
			if !p.MaxScope().Covers(need) {
				err = p.Require(need, "")
			}
		case p.Confined():
			if !instanceRoutesForConfined[pattern] && (p.TokenName != "" || !instanceRoutesForConfinedUsers[pattern]) {
				err = fmt.Errorf("%w: limited to particular projects", auth.ErrForbidden)
			}
		default:
			err = p.Require(need, "")
		}
		if err != nil {
			writeError(w, r, err)
			return
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
	// Single sign-on: whether to offer it, and the round trip through the provider.
	mux.HandleFunc("GET /api/v1/auth/oidc", a.oidcStatus)
	mux.HandleFunc("GET /api/v1/auth/oidc/start", a.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", a.oidcCallback)
	// An invitation link is its own credential.
	mux.HandleFunc("GET /api/v1/invites/{token}", a.invitation)
	mux.HandleFunc("POST /api/v1/invites/{token}", a.acceptInvitation)

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
	rd("POST /api/v1/auth/password", a.changePassword)
	rd("GET /api/v1/auth/ssh-keys", a.mySSHKeys)
	rd("PUT /api/v1/auth/ssh-keys", a.setMySSHKeys)

	rd("GET /api/v1/dashboard", a.dashboard)
	rd("GET /api/v1/runtimes", a.runtimes)
	rd("POST /api/v1/cron/preview", a.cronPreview)
	adm("GET /api/v1/docker", a.dockerOverview)
	adm("GET /api/v1/docker/images/unused", a.unusedImages)
	adm("POST /api/v1/docker/images/prune", a.pruneImages)
	adm("POST /api/v1/docker/orphans/remove", a.removeOrphan)
	rd("GET /api/v1/settings", a.settings)
	adm("PATCH /api/v1/settings", a.updateSettings)
	adm("GET /api/v1/settings/oidc", a.oidcSettings)
	adm("PUT /api/v1/settings/oidc", a.setOIDCSettings)
	adm("POST /api/v1/settings/oidc/test", a.testOIDCSettings)
	adm("GET /api/v1/settings/secret-key", a.secretKey)
	adm("POST /api/v1/settings/secret-key/reveal", a.revealSecretKey)
	adm("POST /api/v1/settings/secret-key/rotate", a.rotateSecretKey)
	adm("GET /api/v1/settings/registries", a.registries)
	adm("GET /api/v1/addons", a.listAddons)
	adm("POST /api/v1/addons", a.installAddon)
	adm("GET /api/v1/addons/{name}", a.getAddon)
	adm("DELETE /api/v1/addons/{name}", a.deleteAddon)
	adm("PUT /api/v1/settings/registries", a.setRegistries)
	adm("GET /api/v1/users", a.listUsers)
	adm("POST /api/v1/users", a.inviteUser)
	adm("PATCH /api/v1/users/{id}", a.updateUser)
	adm("DELETE /api/v1/users/{id}", a.deleteUser)
	adm("POST /api/v1/users/{id}/invite", a.renewInvite)
	adm("PUT /api/v1/users/{id}/projects/{project}", a.setProjectRole)
	rd("GET /api/v1/tokens", a.listTokens)
	rd("POST /api/v1/tokens", a.createToken)
	rd("DELETE /api/v1/tokens/{id}", a.deleteToken)
	adm("DELETE /api/v1/settings/log-history", a.clearLogHistory)
	adm("DELETE /api/v1/settings/metrics", a.clearMetrics)
	rd("GET /api/v1/metrics/overview", a.metricsOverview)
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
	rd("GET /api/v1/projects/{id}/cron", a.listCronJobs)
	adm("POST /api/v1/projects/{id}/cron", a.addCronJob)
	adm("PUT /api/v1/projects/{id}/cron/{job}", a.updateCronJob)
	adm("DELETE /api/v1/projects/{id}/cron/{job}", a.removeCronJob)
	op("POST /api/v1/projects/{id}/cron/{job}/run", a.runCronJob)
	op("GET /api/v1/projects/{id}/cron/{job}/runs", a.cronRuns)
	rd("GET /api/v1/projects/{id}/domains", a.listDomains)
	op("POST /api/v1/projects/{id}/domains", a.addDomain)
	op("DELETE /api/v1/projects/{id}/domains/{domain}", a.removeDomain)
	adm("GET /api/v1/audit", a.auditLog)
	adm("GET /api/v1/audit/export", a.auditExport)
	adm("GET /api/v1/audit/users", a.auditUsers)
	adm("GET /api/v1/audit/settings", a.auditSettings)
	adm("PUT /api/v1/audit/settings", a.setAuditSettings)
	adm("GET /api/v1/instance/backups", a.listInstanceBackups)
	adm("POST /api/v1/instance/backups", a.createInstanceBackup)
	adm("POST /api/v1/instance/backups/upload", a.uploadInstanceBackup)
	adm("DELETE /api/v1/instance/backups/{id}", a.deleteInstanceBackup)
	adm("GET /api/v1/instance/backups/{id}/download", a.downloadInstanceBackup)
	adm("POST /api/v1/instance/backups/{id}/restore", a.restoreInstanceBackup)
	adm("POST /api/v1/instance/backups/{id}/offsite", a.uploadInstanceBackupOffsite)
	adm("GET /api/v1/offsite", a.listOffsiteTargets)
	adm("POST /api/v1/offsite/targets", a.saveOffsiteTarget)
	adm("PUT /api/v1/offsite/targets/{target}", a.saveOffsiteTarget)
	adm("DELETE /api/v1/offsite/targets/{target}", a.deleteOffsiteTarget)
	adm("POST /api/v1/offsite/test", a.testOffsiteTarget)
	adm("GET /api/v1/offsite/targets/{target}/instance", a.listRemoteInstanceBackups)
	adm("POST /api/v1/offsite/targets/{target}/instance/fetch", a.fetchRemoteInstanceBackup)
	adm("POST /api/v1/offsite/targets/{target}/remove", a.deleteRemoteBackup)
	adm("DELETE /api/v1/instance/restore", a.cancelInstanceRestore)
	adm("GET /api/v1/system/reconcile", a.reconcileReport)
	adm("GET /api/v1/system/diagnostics", a.diagnostics)
	adm("POST /api/v1/system/reconcile", a.reconcileNow)

	rd("GET /api/v1/projects", a.listProjects)
	rd("GET /api/v1/operations", a.listOperations)
	adm("POST /api/v1/projects", a.createProject)
	adm("POST /api/v1/projects/preview", a.previewProject)
	adm("POST /api/v1/projects/from-manifest", a.createFromManifest)
	adm("POST /api/v1/site-imports", a.uploadSite)
	adm("GET /api/v1/site-imports/{id}", a.getSiteImport)
	adm("DELETE /api/v1/site-imports/{id}", a.deleteSiteImport)
	rd("GET /api/v1/projects/{id}/manifest", a.getManifest)
	op("PUT /api/v1/projects/{id}/manifest/file", a.writeManifest)
	rd("POST /api/v1/projects/{id}/manifest/plan", a.planManifest)
	adm("POST /api/v1/projects/{id}/manifest/apply", a.applyManifest)
	rd("GET /api/v1/projects/{id}", a.getProject)
	adm("PATCH /api/v1/projects/{id}", a.updateProject)
	adm("DELETE /api/v1/projects/{id}", a.deleteProject)
	adm("POST /api/v1/projects/{id}/duplicate", a.duplicateProject)
	adm("POST /api/v1/projects/{id}/rename", a.renameProject)
	rd("GET /api/v1/projects/{id}/branches", a.listBranchEnvironments)
	adm("PUT /api/v1/projects/{id}/branches/settings", a.setBranchSettings)
	op("GET /api/v1/projects/{id}/branches/remote", a.remoteBranches)
	adm("POST /api/v1/projects/{id}/branches", a.createBranchEnvironment)
	op("POST /api/v1/projects/{id}/deploy", a.deployBranchEnvironment)
	op("POST /api/v1/projects/{id}/start", a.startProject)
	op("POST /api/v1/projects/{id}/stop", a.stopProject)
	op("POST /api/v1/projects/{id}/restart", a.restartProject)
	op("POST /api/v1/projects/{id}/images", a.useImage)
	adm("PUT /api/v1/projects/{id}/services/{kind}/image", a.setCustomImage)
	rd("GET /api/v1/projects/{id}/addons", a.projectAddons)
	op("PUT /api/v1/projects/{id}/addons/{name}", a.setProjectAddon)
	adm("DELETE /api/v1/projects/{id}/services/{kind}/image", a.removeCustomImage)
	op("POST /api/v1/projects/{id}/services/{kind}/image/build", a.rebuildCustomImage)
	rd("GET /api/v1/package-cache", a.packageCache)
	adm("DELETE /api/v1/package-cache", a.clearPackageCache)
	op("GET /api/v1/dbtool", a.dbToolStatus)
	adm("PUT /api/v1/dbtool", a.setDBTool)
	op("POST /api/v1/projects/{id}/dbtool", a.openDBTool)
	// The database browser lives inside the UI origin so the session protects it.
	mux.Handle(project.DBToolPathPrefix+"/", protect(a.guard(auth.ScopeOperate, project.DBToolPathPrefix+"/", newDBToolProxy(a.d.Projects).ServeHTTP)))
	rd("GET /api/v1/projects/{id}/services", a.projectServices)
	rd("GET /api/v1/projects/{id}/plan", a.projectPlan)
	rd("GET /api/v1/projects/{id}/stats", a.projectStats)
	adm("PUT /api/v1/projects/{id}/limits", a.setLimits)
	adm("PUT /api/v1/projects/{id}/health-check", a.setHealthCheck)
	adm("PUT /api/v1/projects/{id}/proxy-rules", a.setProxyRules)
	adm("POST /api/v1/projects/{id}/health-check/test", a.testHealthCheck)
	rd("GET /api/v1/projects/{id}/metrics", a.projectMetrics)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs", a.serviceLogs)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs/ws", a.serviceLogsWS)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs/stats", a.serviceLogsStats)
	rd("GET /api/v1/projects/{id}/services/{kind}/logs/download", a.serviceLogsDownload)
	op("GET /api/v1/projects/{id}/services/{kind}/terminal/ws", a.terminalWS)
	op("POST /api/v1/projects/{id}/services/{kind}/exec", a.exec)
	rd("GET /api/v1/projects/{id}/git", a.gitStatus)
	op("PUT /api/v1/projects/{id}/git", a.gitSet)
	op("POST /api/v1/projects/{id}/git/clone", a.gitClone)
	op("POST /api/v1/projects/{id}/git/pull", a.gitPull)
	op("POST /api/v1/projects/{id}/git/checkout", a.gitCheckout)
	adm("GET /api/v1/settings/deploy-key", a.deployKey)
	adm("POST /api/v1/settings/deploy-key/regenerate", a.deployKeyRegenerate)
	rd("GET /api/v1/projects/{id}/actions", a.listActions)
	op("GET /api/v1/projects/{id}/actions/{action}/ws", a.actionWS)
	rd("GET /api/v1/projects/{id}/share", a.getShare)
	adm("POST /api/v1/projects/{id}/share", a.startShare)
	op("DELETE /api/v1/projects/{id}/share", a.stopShare)
	rd("GET /api/v1/projects/{id}/tests", a.listTests)
	rd("GET /api/v1/projects/{id}/test-runs/{run}", a.getTestRun)
	op("GET /api/v1/projects/{id}/tests/{suite}/ws", a.testWS)
	rd("GET /api/v1/projects/{id}/backups", a.listBackups)
	op("POST /api/v1/projects/{id}/backups/{backup}/offsite", a.uploadBackupOffsite)
	op("GET /api/v1/projects/{id}/offsite/{target}", a.listRemoteBackups)
	op("POST /api/v1/projects/{id}/offsite/{target}/fetch", a.fetchRemoteBackup)
	adm("PUT /api/v1/projects/{id}/backups/schedule", a.setBackupSchedule)
	op("POST /api/v1/projects/{id}/backups", a.createBackup)
	adm("DELETE /api/v1/projects/{id}/backups/{backup}", a.deleteBackup)
	adm("POST /api/v1/projects/{id}/backups/{backup}/restore", a.restoreBackup)
	op("GET /api/v1/projects/{id}/backups/{backup}/download", a.downloadBackup)
	rd("GET /api/v1/projects/{id}/extras", a.extraServices)
	adm("DELETE /api/v1/projects/{id}/extras/{kind}/data", a.deleteKeptData)
	op("GET /api/v1/projects/{id}/rabbitmq/credentials", a.rabbitMQCredentials)
	op("GET /api/v1/projects/{id}/meilisearch/credentials", a.searchCredentials(store.ServiceMeilisearch))
	op("GET /api/v1/projects/{id}/typesense/credentials", a.searchCredentials(store.ServiceTypesense))
	// The model store is shared by every project: deleting a model is an admin matter.
	// Trying a connection runs a client container against any address: admin only.
	adm("POST /api/v1/external/test", a.testExternal)
	rd("GET /api/v1/projects/{id}/ollama/models", a.ollamaModels)
	op("POST /api/v1/projects/{id}/ollama/models", a.pullOllamaModel)
	adm("DELETE /api/v1/projects/{id}/ollama/models/{model...}", a.deleteOllamaModel)
	op("DELETE /api/v1/projects/{id}/ollama/pulls/{model...}", a.cancelOllamaPull)
	rd("GET /api/v1/projects/{id}/storage", a.storageInfo)
	op("GET /api/v1/projects/{id}/storage/credentials", a.storageCredentials)
	adm("PUT /api/v1/projects/{id}/storage/public", a.storagePublic)
	rd("GET /api/v1/projects/{id}/databases", a.databases)
	rd("GET /api/v1/projects/{id}/database", a.databaseInfo)
	op("GET /api/v1/projects/{id}/database/credentials", a.databaseCredentials)
	adm("POST /api/v1/projects/{id}/database/rotate", a.databaseRotate)
	adm("POST /api/v1/projects/{id}/database/expose", a.databaseExpose)
	rd("GET /api/v1/projects/{id}/database/databases", a.databaseList)
	op("POST /api/v1/projects/{id}/database/databases", a.databaseCreate)
	adm("DELETE /api/v1/projects/{id}/database/databases/{name}", a.databaseDrop)
	rd("GET /api/v1/projects/{id}/database/snapshots", a.databaseSnapshots)
	op("POST /api/v1/projects/{id}/database/snapshots", a.databaseSnapshotCreate)
	adm("POST /api/v1/projects/{id}/database/snapshots/{snapshot}/restore", a.databaseSnapshotRestore)
	adm("POST /api/v1/projects/{id}/database/clone", a.databaseClone)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, newError(http.StatusNotFound, "not_found", "unknown API route"))
	})
}

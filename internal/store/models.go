// Package store contains the SQLite repositories for Envoryx's persistent state.
package store

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// User is an Envoryx login account.
type User struct {
	ID       string
	Username string
	// PasswordHash is empty for a user who has not accepted an invitation yet and for one
	// who only signs in through OpenID Connect.
	PasswordHash string
	// Role is admin, developer, viewer or none (see auth.Role).
	Role string
	// InviteHash is the hash of an open invitation's token, InviteExpiresAt its end.
	InviteHash      string
	InviteExpiresAt time.Time
	// OIDCSubject links the user to an OpenID Connect account (issuer subject).
	OIDCSubject string
	// Disabled users can neither sign in nor use their API tokens.
	Disabled bool
	// SSHKeys are the user's public keys for the SSH server, authorized_keys format.
	SSHKeys   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is a server-side login session. ID is the SHA-256 hash of the opaque token.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	IP         string
	UserAgent  string
}

// DesiredState is what the user wants a project to be.
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

// Lifecycle tracks long-running transitions of a project record.
type Lifecycle string

const (
	LifecycleCreating Lifecycle = "creating"
	LifecycleReady    Lifecycle = "ready"
	LifecycleDeleting Lifecycle = "deleting"
	LifecycleFailed   Lifecycle = "failed"
)

// ServiceKind identifies the role of a service inside a project.
type ServiceKind string

const (
	ServiceWeb         ServiceKind = "web"
	ServicePHP         ServiceKind = "php"
	ServiceNode        ServiceKind = "node"
	ServicePython      ServiceKind = "python"
	ServiceGo          ServiceKind = "go"
	ServiceRuby        ServiceKind = "ruby"
	ServiceJava        ServiceKind = "java"
	ServiceDotnet      ServiceKind = "dotnet"
	ServiceDatabase    ServiceKind = "database"
	ServiceRedis       ServiceKind = "redis"
	ServiceMailpit     ServiceKind = "mailpit"
	ServiceRabbitMQ    ServiceKind = "rabbitmq"
	ServiceMemcached   ServiceKind = "memcached"
	ServiceMeilisearch ServiceKind = "meilisearch"
	ServiceTypesense   ServiceKind = "typesense"
	ServiceOpenSearch  ServiceKind = "opensearch"
	ServiceOllama      ServiceKind = "ollama"
	// ServiceOpenSearchDashboards belongs to ServiceOpenSearch: same version, and it goes
	// when OpenSearch goes.
	ServiceOpenSearchDashboards ServiceKind = "opensearch-dashboards"
	ServiceStorage              ServiceKind = "storage"
)

// Project is the persisted desired state of a development project.
type Project struct {
	ID           string
	Name         string
	Slug         string
	Path         string // relative to the projects root
	Docroot      string // relative to Path, may be empty
	DesiredState DesiredState
	HTTPPort     int // 0 when none is allocated
	Lifecycle    Lifecycle
	LastError    string
	Git          GitConfig
	Backup       BackupSchedule
	// IDEGateway allows JetBrains Gateway sessions: SSH port forwarding into the
	// application containers and a shared IDE backend cache mount.
	IDEGateway bool
	// Limits caps CPU, memory and processes of the project's containers.
	Limits ResourceLimits
	// HealthCheck asks the application over HTTP whether it works; an empty Path
	// switches it off.
	HealthCheck HealthCheck
	// ProxyRules are the redirects, headers, CORS and access rules of its host names.
	ProxyRules ProxyRules
	// ParentID names the project a branch environment was made from; empty for every
	// other project. Branches are the parent's settings for its environments,
	// BranchState an environment's deploy state.
	ParentID    string
	Branches    BranchSettings
	BranchState BranchState
	CreatedAt   time.Time
	UpdatedAt   time.Time

	Services []ProjectService
	Env      []EnvVar
	Workers  []Worker
	// Images is the image history (rollback state) per image reference.
	Images []ProjectImage
}

// ImageRecord returns the history entry for an image reference, if any.
func (p Project) ImageRecord(image string) *ProjectImage {
	for i := range p.Images {
		if p.Images[i].Image == image {
			return &p.Images[i]
		}
	}
	return nil
}

// LimitSet caps one group of containers; zero values mean no limit.
type LimitSet struct {
	// CPUs is the number of cores each container may use (1.5 = one and a half).
	CPUs float64 `json:"cpus,omitempty"`
	// MemoryMB is the memory of each container in MiB.
	MemoryMB int `json:"memoryMb,omitempty"`
}

// IsZero reports a set without limits.
func (l LimitSet) IsZero() bool { return l.CPUs == 0 && l.MemoryMB == 0 }

// ResourceLimits are the limits of a project: one set for the application containers
// (web server, PHP, Node, Python, Go, Ruby, workers), one for the services (database,
// Redis, search, storage …), and the process limit of every container (0 means Envoryx's
// default).
type ResourceLimits struct {
	App      LimitSet `json:"app"`
	Services LimitSet `json:"services"`
	Pids     int      `json:"pids,omitempty"`
}

// IsZero reports limits that were never set.
func (l ResourceLimits) IsZero() bool { return l.App.IsZero() && l.Services.IsZero() && l.Pids == 0 }

func (l ResourceLimits) encode() string {
	if l.IsZero() {
		return ""
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// HealthCheck is an HTTP request Envoryx sends to a project's application at an interval;
// the application is down when it fails several times in a row. Zero values except Path
// mean Envoryx's defaults.
type HealthCheck struct {
	// Path is what is requested, with an optional query ("/health", "/up?full=1").
	Path string `json:"path,omitempty"`
	// Status is the expected HTTP status (default 200).
	Status int `json:"status,omitempty"`
	// IntervalSec is the time between two checks (default 30).
	IntervalSec int `json:"intervalSec,omitempty"`
	// TimeoutSec is how long one answer may take (default 5).
	TimeoutSec int `json:"timeoutSec,omitempty"`
	// Failures is how many failed checks in a row make the application down (default 3).
	Failures int `json:"failures,omitempty"`
}

// Health check defaults.
const (
	DefaultHealthStatus   = 200
	DefaultHealthInterval = 30
	DefaultHealthTimeout  = 5
	DefaultHealthFailures = 3
)

// Enabled reports a configured check.
func (h HealthCheck) Enabled() bool { return h.Path != "" }

// WithDefaults fills the unset values.
func (h HealthCheck) WithDefaults() HealthCheck {
	if h.Status == 0 {
		h.Status = DefaultHealthStatus
	}
	if h.IntervalSec == 0 {
		h.IntervalSec = DefaultHealthInterval
	}
	if h.TimeoutSec == 0 {
		h.TimeoutSec = DefaultHealthTimeout
	}
	if h.Failures == 0 {
		h.Failures = DefaultHealthFailures
	}
	return h
}

func (h HealthCheck) encode() string {
	if !h.Enabled() {
		return ""
	}
	b, _ := json.Marshal(h)
	return string(b)
}

// ProxyRules are what the embedded proxy does with requests for a project's host names
// (its default name, extra domains, dev server name and share address) before they
// reach the application. The zero value does nothing.
type ProxyRules struct {
	// AllowIPs admits only these addresses and networks ("192.168.1.0/24"); empty admits all.
	AllowIPs []string `json:"allowIPs,omitempty"`
	// BasicAuth asks for a user name and password.
	BasicAuth *BasicAuthRule `json:"basicAuth,omitempty"`
	// Redirects are tried in order; the first that matches answers.
	Redirects []RedirectRule `json:"redirects,omitempty"`
	// Headers are set on every response (an empty value removes the header).
	Headers []HeaderRule `json:"headers,omitempty"`
	// CORS answers preflight requests and adds the CORS headers for these origins.
	CORS *CORSRule `json:"cors,omitempty"`
}

// BasicAuthRule is HTTP basic authentication in front of a project.
type BasicAuthRule struct {
	User string `json:"user"`
	// PasswordHash is a bcrypt hash; the password itself is not kept.
	PasswordHash string `json:"passwordHash"`
}

// RedirectRule answers requests for a path with a redirect. From is a path, or a prefix
// ending in "*"; a To ending in "*" gets the rest of the path.
type RedirectRule struct {
	// Host limits the rule to one host name; empty means all of the project's.
	Host   string `json:"host,omitempty"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status int    `json:"status"`
}

// HeaderRule is a response header.
type HeaderRule struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CORSRule lets pages on other origins call the project.
type CORSRule struct {
	// Origins are "https://app.test", "https://*.shop.test" or "*".
	Origins []string `json:"origins"`
	// Methods default to GET, POST, PUT, PATCH, DELETE and OPTIONS.
	Methods []string `json:"methods,omitempty"`
	// Headers are the request headers allowed; empty allows whatever the browser asks
	// for.
	Headers     []string `json:"headers,omitempty"`
	Credentials bool     `json:"credentials,omitempty"`
	MaxAgeSec   int      `json:"maxAgeSec,omitempty"`
}

// Empty reports rules that do nothing.
func (r ProxyRules) Empty() bool {
	return len(r.AllowIPs) == 0 && r.BasicAuth == nil && len(r.Redirects) == 0 && len(r.Headers) == 0 && r.CORS == nil
}

func (r ProxyRules) encode() string {
	if r.Empty() {
		return ""
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// BranchSettings are a project's rules for its branch environments. The zero value
// watches nothing: environments are then only made and removed by hand.
type BranchSettings struct {
	// Watch polls the repository (git ls-remote): a push to an environment's branch is
	// pulled and deployed, an environment whose branch is gone is deleted, and a new
	// branch that matches Patterns gets an environment.
	Watch bool `json:"watch,omitempty"`
	// Patterns are the branches that get an environment of their own while Watch is on
	// ("feature/*", path.Match syntax); empty creates none automatically.
	Patterns []string `json:"patterns,omitempty"`
	// PollMinutes is how often the repository is asked; 0 means the default.
	PollMinutes int `json:"pollMinutes,omitempty"`
	// IdleStopDays stops an environment nobody has opened for that many days; 0 never.
	IdleStopDays int `json:"idleStopDays,omitempty"`
	// Deploy are the shell commands run in an environment's application container after
	// it was created or pulled (composer install, php artisan migrate, npm ci …).
	Deploy []string `json:"deploy,omitempty"`
	// MaxEnvironments caps the environments Watch creates; 0 means the default.
	MaxEnvironments int `json:"maxEnvironments,omitempty"`
}

// Empty reports whether nothing is configured.
func (b BranchSettings) Empty() bool {
	return !b.Watch && len(b.Patterns) == 0 && b.PollMinutes == 0 && b.IdleStopDays == 0 && len(b.Deploy) == 0 && b.MaxEnvironments == 0
}

func (b BranchSettings) encode() string {
	if b.Empty() {
		return ""
	}
	j, _ := json.Marshal(b)
	return string(j)
}

// Deploy states of a branch environment.
const (
	DeployRunning   = "running"
	DeploySucceeded = "succeeded"
	DeployFailed    = "failed"
)

// BranchState is what a branch environment last deployed and when it was last used.
type BranchState struct {
	// Commit is the commit the environment was last deployed at.
	Commit string `json:"commit,omitempty"`
	// DeployStatus is DeployRunning, DeploySucceeded or DeployFailed; empty before the
	// first deploy.
	DeployStatus string    `json:"deployStatus,omitempty"`
	DeployedAt   time.Time `json:"deployedAt,omitzero"`
	// DeployOutput is the end of what the last pull and deploy commands printed.
	DeployOutput string `json:"deployOutput,omitempty"`
	// LastAccess is the last request through the proxy (or start from the UI); the idle
	// stop counts from it.
	LastAccess time.Time `json:"lastAccess,omitzero"`
}

func (b BranchState) encode() string {
	if b == (BranchState{}) {
		return ""
	}
	j, _ := json.Marshal(b)
	return string(j)
}

// BackupSchedule configures automatic backups of a project.
type BackupSchedule struct {
	// Schedule is "daily", "weekly" or "" for off.
	Schedule string
	// Hour of day (local time) the backup runs at.
	Hour int
	// Weekday for weekly schedules, 0 being Sunday.
	Weekday int
	// Keep is how many scheduled backups are retained (older ones are deleted).
	Keep int
	// IncludeDependencies keeps vendor/ and node_modules/ in the file archive.
	IncludeDependencies bool
	// LastRun is when the schedule last produced a backup, zero if it never has.
	LastRun time.Time
}

// GitConfig is the optional repository binding of a project. Token is a secret and must
// never be part of API responses or logs.
type GitConfig struct {
	URL      string
	Branch   string
	Username string
	Token    string
}

// Additional databases are services of their own kind, "db-<name>": the kind names the
// container (envoryx-<slug>-db-<name>) and the volume, and the primary database keeps
// ServiceDatabase with everything that hangs on it.
const extraDatabasePrefix = "db-"

// DatabaseKind returns the service kind of a project database: the primary for "", an
// additional one by its name.
func DatabaseKind(name string) ServiceKind {
	if name == "" {
		return ServiceDatabase
	}
	return ServiceKind(extraDatabasePrefix + name)
}

// addonPrefix starts the kind of an addon's service: addon-<name>.
const addonPrefix = "addon-"

// AddonKind is the service kind of an addon.
func AddonKind(name string) ServiceKind { return ServiceKind(addonPrefix + name) }

// AddonName is the addon of an addon service ("" for every other kind).
func (k ServiceKind) AddonName() string {
	name, _ := strings.CutPrefix(string(k), addonPrefix)
	if name == string(k) {
		return ""
	}
	return name
}

// IsAddon reports an addon's service.
func (k ServiceKind) IsAddon() bool { return strings.HasPrefix(string(k), addonPrefix) }

// Addons returns the project's addon services, by name.
func (p *Project) Addons() []*ProjectService {
	var out []*ProjectService
	for i := range p.Services {
		if p.Services[i].Kind.IsAddon() {
			out = append(out, &p.Services[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// IsDatabase reports the primary database and the additional ones.
func (k ServiceKind) IsDatabase() bool {
	return k == ServiceDatabase || strings.HasPrefix(string(k), extraDatabasePrefix)
}

// DatabaseName is the name of an additional database ("" for the primary and for every
// other kind).
func (k ServiceKind) DatabaseName() string {
	if name, ok := strings.CutPrefix(string(k), extraDatabasePrefix); ok {
		return name
	}
	return ""
}

// Databases returns the enabled databases of the project, the primary first, then the
// additional ones by name.
func (p *Project) Databases() []*ProjectService {
	var out []*ProjectService
	for i := range p.Services {
		if p.Services[i].Kind.IsDatabase() && p.Services[i].Enabled {
			out = append(out, &p.Services[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Kind, out[j].Kind
		if a == ServiceDatabase || b == ServiceDatabase {
			return a == ServiceDatabase && b != ServiceDatabase
		}
		return a < b
	})
	return out
}

// Service returns the service of the given kind, or nil.
func (p *Project) Service(kind ServiceKind) *ProjectService {
	for i := range p.Services {
		if p.Services[i].Kind == kind {
			return &p.Services[i]
		}
	}
	return nil
}

// ProjectService is one component (container) of a project.
type ProjectService struct {
	ID        string
	ProjectID string
	Kind      ServiceKind
	Variant   string // e.g. "caddy", "mariadb"
	Version   string // catalogue version key, e.g. "8.4"
	Image     string // resolved image reference
	Enabled   bool
	Config    json.RawMessage // kind-specific configuration
	Position  int
	// Custom replaces the catalogue image of a runtime service; the zero value keeps it.
	Custom CustomImage
}

// CustomImage is an image of the user's for a runtime service: a registry reference, or
// a Dockerfile in the project that Envoryx builds (its directory is the build context).
type CustomImage struct {
	Image      string `json:"image,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	// Warnings are what the last check of the image found missing.
	Warnings []string `json:"warnings,omitempty"`
	// CheckedImage is the image the warnings belong to (a built tag changes with the
	// Dockerfile), CheckedAt when it was checked.
	CheckedImage string    `json:"checkedImage,omitempty"`
	CheckedAt    time.Time `json:"checkedAt,omitzero"`
	// BuildOutput is the end of the last build's output, BuildFailed whether it failed.
	BuildOutput string `json:"buildOutput,omitempty"`
	BuildFailed bool   `json:"buildFailed,omitempty"`
}

// Set reports whether a custom image is configured.
func (c CustomImage) Set() bool { return c.Image != "" || c.Dockerfile != "" }

func (c CustomImage) encode() string {
	if !c.Set() {
		return ""
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// EnvVar is a project environment variable.
type EnvVar struct {
	ID        string
	ProjectID string
	Key       string
	Value     string
	IsSecret  bool
	CreatedAt time.Time
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID         string
	CreatedAt  time.Time
	UserID     string
	Username   string
	Action     string
	TargetType string
	TargetID   string
	Details    json.RawMessage
	IP         string
}

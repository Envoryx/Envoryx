// Package store contains the SQLite repositories for Envoryx's persistent state.
package store

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// User is a Envoryx login account.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	ServiceDatabase    ServiceKind = "database"
	ServiceRedis       ServiceKind = "redis"
	ServiceMailpit     ServiceKind = "mailpit"
	ServiceRabbitMQ    ServiceKind = "rabbitmq"
	ServiceMemcached   ServiceKind = "memcached"
	ServiceMeilisearch ServiceKind = "meilisearch"
	ServiceTypesense   ServiceKind = "typesense"
	ServiceOpenSearch  ServiceKind = "opensearch"
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
	HTTPPort     int // 0 = none allocated
	Lifecycle    Lifecycle
	LastError    string
	Git          GitConfig
	Backup       BackupSchedule
	// IDEGateway allows JetBrains Gateway sessions: SSH port forwarding into the
	// application containers and a shared IDE backend cache mount.
	IDEGateway bool
	// Limits caps CPU, memory and processes of the project's containers.
	Limits ResourceLimits
	// HealthCheck asks the application over HTTP whether it works (Path "" = off).
	HealthCheck HealthCheck
	// ProxyRules are the redirects, headers, CORS and access rules of its host names.
	ProxyRules ProxyRules
	CreatedAt  time.Time
	UpdatedAt  time.Time

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
// (web server, PHP, Node, Python, workers), one for the services (database, Redis,
// search, storage …), and the process limit of every container (0 = Envoryx's default).
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
	// AllowIPs admits only these addresses and networks ("192.168.1.0/24"); empty = all.
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
	// Host limits the rule to one host name ("" = all of the project's).
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
	// Headers are the request headers allowed (empty = whatever the browser asks for).
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

// BackupSchedule configures automatic backups of a project.
type BackupSchedule struct {
	// Schedule is "" (off), "daily" or "weekly".
	Schedule string
	// Hour of day (local time) the backup runs at.
	Hour int
	// Weekday for weekly schedules (0 = Sunday).
	Weekday int
	// Keep is how many scheduled backups are retained (older ones are deleted).
	Keep int
	// IncludeDependencies keeps vendor/ and node_modules/ in the file archive.
	IncludeDependencies bool
	// LastRun is when the schedule last produced a backup (zero = never).
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

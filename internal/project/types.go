// Package project implements the project manager: planning, lifecycle operations with
// rollback, status derivation and reconciliation between database and Docker.
package project

import (
	"errors"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// Domain errors mapped to HTTP status codes by the API layer.
var (
	ErrNotFound      = store.ErrNotFound
	ErrConflict      = store.ErrConflict
	ErrBusy          = errors.New("project is busy with another operation")
	ErrDockerDown    = docker.ErrUnavailable
	ErrNotConfigured = errors.New("envoryx is not fully configured")
)

// CreateRequest is the validated intent to create a project.
type CreateRequest struct {
	Name string
	Path string // relative to projects root; empty = slug
	// Docroot is the directory served by the web server, relative to the project
	// directory: public/ for Laravel/Symfony, the build output (dist/, out/) for static
	// Node builds; unused while a Python server or Node dev server serves the app.
	Docroot  string
	PHP      *PHPRequest
	Node     *NodeRequest
	Python   *PythonRequest
	Database *DatabaseRequest
	// Databases are additional databases next to the primary one, each with a name of
	// its own (host, container and variables follow it).
	Databases   []NamedDatabaseRequest
	Redis       *ExtraRequest
	Memcached   *ExtraRequest
	Mailpit     *ExtraRequest
	RabbitMQ    *ExtraRequest
	Meilisearch *ExtraRequest
	Typesense   *ExtraRequest
	Ollama      *ExtraRequest
	OpenSearch  *ExtraRequest
	Storage     *StorageRequest
	Web         WebRequest
	Git         *GitRequest
	Env         []EnvVarRequest
	// Template scaffolds an application into the new directory (see Templates()).
	Template string
	// Import fills the new directory from an uploaded website (see CreateFromImport).
	Import *ImportRequest
	// CreateStarter writes a starter page (index.php with PHP, index.html otherwise) when
	// the document root is empty; ignored while a Python server or Node dev server serves
	// the app.
	CreateStarter bool
	// Start starts the project right after creation.
	Start bool
	// Limits cap CPU, memory and processes of the containers (zero: none).
	Limits store.ResourceLimits
	// HealthCheck asks the application over HTTP whether it works (Path "": none).
	HealthCheck store.HealthCheck
}

// PHPRequest selects the PHP runtime.
type PHPRequest struct {
	Version string
	Config  runtime.PHPConfig
}

// PHPUpdate changes, adds (Enabled on a project without PHP) or removes (Enabled false)
// the PHP service.
type PHPUpdate struct {
	Enabled bool
	Version string
	Config  runtime.PHPConfig
}

// NodeRequest selects the Node.js toolchain container and optional dev server.
type NodeRequest struct {
	Version string
	Config  runtime.NodeConfig
}

// NodeUpdate adds, changes or removes the Node.js service.
type NodeUpdate struct {
	Enabled bool
	Version string
	Config  runtime.NodeConfig
}

// PythonRequest selects the Python container and optional application server.
type PythonRequest struct {
	Version string
	Config  runtime.PythonConfig
}

// PythonUpdate adds, changes or removes the Python service.
type PythonUpdate struct {
	Enabled bool
	Version string
	Config  runtime.PythonConfig
}

// ExtraRequest selects an auxiliary service (Redis, Memcached, Mailpit, RabbitMQ,
// Meilisearch, Typesense, OpenSearch, Ollama).
type ExtraRequest struct {
	Version    string
	ExposePort bool
	// Dashboards adds OpenSearch Dashboards (OpenSearch only).
	Dashboards bool
	// GPU hands the host's GPUs to the service (Ollama only).
	GPU bool
}

// StorageRequest adds S3-compatible object storage. PublicRead (default true) lets anyone
// read the bucket's objects, as public-read ACLs do on providers that honour them.
type StorageRequest struct {
	Version    string
	PublicRead *bool
}

// StorageUpdate adds, changes or removes the object storage.
type StorageUpdate struct {
	Enabled    bool
	Version    string
	PublicRead *bool
	// RemoveData confirms deleting the bucket volume when disabling.
	RemoveData bool
}

// ExtraUpdate adds, changes or removes an auxiliary service.
type ExtraUpdate struct {
	Enabled    bool
	Version    string
	ExposePort bool
	// RemoveData must be true to remove a service that owns a volume (Redis, RabbitMQ,
	// Meilisearch, Typesense, OpenSearch).
	RemoveData bool
	// Dashboards switches OpenSearch Dashboards on or off (OpenSearch only); nil leaves it
	// as it is.
	Dashboards *bool
	// GPU switches the host's GPUs on or off (Ollama only); nil leaves it as it is.
	GPU *bool
}

// DatabaseRequest selects a database service.
type DatabaseRequest struct {
	Type       string // "mariadb"
	Version    string
	ExposePort bool // publish the database on a host port for external clients
}

// NamedDatabaseRequest is an additional database of a new project.
type NamedDatabaseRequest struct {
	Name string
	DatabaseRequest
}

// DatabaseUpdate changes the database service of an existing project.
type DatabaseUpdate struct {
	// Enabled adds (true) or removes (false) the database service.
	Enabled    bool
	Type       string
	Version    string
	ExposePort bool
	// RemoveData must be true to remove a database together with its volume.
	RemoveData bool
}

// WebRequest selects the web server.
type WebRequest struct {
	Type    string // "caddy" (default), "apache" or "nginx"
	Version string
	// SPAFallback serves /index.html for unknown paths (nil = unchanged, false on create).
	// Only valid for projects without PHP: there the front controller handles unknown paths.
	SPAFallback *bool
}

// EnvVarRequest is one environment variable.
type EnvVarRequest struct {
	Key      string
	Value    string
	IsSecret bool
}

// UpdateRequest changes editable project settings. Nil pointers leave fields untouched.
type UpdateRequest struct {
	Name     *string
	Docroot  *string
	Web      *WebRequest
	PHP      *PHPUpdate
	Node     *NodeUpdate
	Python   *PythonUpdate
	Database *DatabaseUpdate
	// Databases adds, changes or removes (Enabled false) additional databases by name.
	Databases   map[string]DatabaseUpdate
	Redis       *ExtraUpdate
	Memcached   *ExtraUpdate
	Mailpit     *ExtraUpdate
	RabbitMQ    *ExtraUpdate
	Meilisearch *ExtraUpdate
	Typesense   *ExtraUpdate
	Ollama      *ExtraUpdate
	OpenSearch  *ExtraUpdate
	Storage     *StorageUpdate
	Env         *[]EnvVarRequest
	// IDEGateway toggles JetBrains Gateway support (port forwarding + shared IDE cache).
	IDEGateway *bool
}

// ExtraServiceInfo describes an auxiliary service for the UI.
type ExtraServiceInfo struct {
	Kind        store.ServiceKind `json:"kind"`
	Version     string            `json:"version"`
	Image       string            `json:"image"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	HostPort    int               `json:"hostPort"`
	InjectedEnv []string          `json:"injectedEnv"`
	State       string            `json:"state"`
	Health      string            `json:"health,omitempty"`
	VolumeName  string            `json:"volumeName,omitempty"`
	// WebUIPort is the host port of a web interface (Mailpit inbox, RabbitMQ management,
	// Meilisearch dashboard, OpenSearch Dashboards), 0 otherwise.
	WebUIPort int `json:"webUiPort,omitempty"`
	// Dashboards is the state of OpenSearch Dashboards when the project has it.
	Dashboards *DashboardsInfo `json:"dashboards,omitempty"`
	// Username logs in to RabbitMQ (AMQP and management UI); the password only comes from
	// Manager.RabbitMQCredentials.
	Username string `json:"username,omitempty"`
	// GPU reports whether Ollama was handed the host's GPUs.
	GPU bool `json:"gpu,omitempty"`
	// External marks a server Envoryx does not run (Redis): Host and Port are its
	// address, and there is no container, volume or published port.
	External bool `json:"external,omitempty"`
}

// RabbitMQCredentials are the login of a project's RabbitMQ broker.
type RabbitMQCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	URL      string `json:"url"` // as injected into the application (RABBITMQ_URL)
}

// DashboardsInfo describes the OpenSearch Dashboards container; its port is the
// OpenSearch entry's WebUIPort.
type DashboardsInfo struct {
	Image  string `json:"image"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

// SearchCredentials are the admin key of a project's Meilisearch or Typesense.
type SearchCredentials struct {
	APIKey string `json:"apiKey"`
	URL    string `json:"url"` // inside the project network, as injected into the application
}

// StorageInfo describes the object storage service. Credentials are included only when
// requested (see Manager.StorageInfo).
type StorageInfo struct {
	Version string `json:"version"`
	Image   string `json:"image"`
	// Endpoint is the S3 URL as seen from application containers; PublicURL the
	// browser-reachable bucket URL through the embedded proxy; HostEndpoint the
	// published S3 port on the host (empty when not published).
	Endpoint    string   `json:"endpoint"`
	PublicURL   string   `json:"publicUrl"`
	HostPort    int      `json:"hostPort"`
	ConsolePort int      `json:"consolePort"`
	ConsolePath string   `json:"consolePath"`
	Region      string   `json:"region"`
	Bucket      string   `json:"bucket"`
	PublicRead  bool     `json:"publicRead"`
	AccessKey   string   `json:"accessKey,omitempty"`
	SecretKey   string   `json:"secretKey,omitempty"`
	InjectedEnv []string `json:"injectedEnv"`
	State       string   `json:"state"`
	Health      string   `json:"health,omitempty"`
	VolumeName  string   `json:"volumeName"`
	Hostname    string   `json:"hostname"`
}

// DatabaseInfo describes the database service without secrets.
type DatabaseInfo struct {
	// Name is "" for the primary database and the name of an additional one.
	Name string `json:"name"`
	// Service is the service kind: "database" or "db-<name>".
	Service      string   `json:"service"`
	Type         string   `json:"type"`
	Version      string   `json:"version"`
	Image        string   `json:"image"`
	Host         string   `json:"host"` // internal DNS name
	Port         int      `json:"port"`
	Database     string   `json:"database"`
	Username     string   `json:"username"`
	HostPort     int      `json:"hostPort"` // 0 = not published
	InjectedEnv  []string `json:"injectedEnv"`
	State        string   `json:"state"`
	Health       string   `json:"health,omitempty"`
	VolumeName   string   `json:"volumeName"`
	VolumeExists bool     `json:"volumeExists"`
}

// DatabaseCredentials are returned only by the explicit credentials endpoint.
type DatabaseCredentials struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	RootPassword string `json:"rootPassword"`
	HostPort     int    `json:"hostPort"`
	URL          string `json:"url"`
}

// DeleteOptions control project deletion.
type DeleteOptions struct {
	// Confirm must equal the project slug.
	Confirm string
	// DeleteFiles removes the project directory as well.
	DeleteFiles bool
}

// State is the derived runtime state of a project.
type State string

const (
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StatePartial  State = "partial"
	StateMissing  State = "missing"
	StateError    State = "error"
	StateCreating State = "creating"
	StateDeleting State = "deleting"
)

// ServiceStatus is the observed state of one project service.
type ServiceStatus struct {
	Kind          store.ServiceKind `json:"kind"`
	Variant       string            `json:"variant"`
	Version       string            `json:"version"`
	Image         string            `json:"image"`
	ContainerName string            `json:"containerName"`
	ContainerID   string            `json:"containerId,omitempty"`
	Exists        bool              `json:"exists"`
	Running       bool              `json:"running"`
	State         string            `json:"state"`
	Status        string            `json:"status,omitempty"`
	Health        string            `json:"health,omitempty"`
	// WorkerID is set for worker containers (Kind "worker", Variant = worker name).
	WorkerID string `json:"workerId,omitempty"`
	Ports    []docker.PortMapping
	// ImageChangedAt is when the containers were last recreated from a rebuilt image of
	// the same reference; ImagePrevious reports that the image before that is still known
	// (rollback possible), ImagePinned that the containers run it on purpose.
	ImageChangedAt *time.Time `json:"imageChangedAt,omitempty"`
	ImagePrevious  bool       `json:"imagePrevious"`
	ImagePinned    bool       `json:"imagePinned"`
}

// Status is the derived state of a project.
type Status struct {
	State    State           `json:"state"`
	Services []ServiceStatus `json:"services"`
	Warnings []string        `json:"warnings"`
	// Operation is the lifecycle action currently running on the project, if any.
	Operation *Operation `json:"operation,omitempty"`
	// Health is the application health check's state (absent without a check).
	Health *HealthStatus `json:"health,omitempty"`
}

// View is a project together with its derived status.
type View struct {
	Project store.Project
	Status  Status
	// HTTPPort is the host port of the project's web server (0 = none).
	HTTPPort int
}

// Preview is what the wizard shows before creating a project.
type Preview struct {
	Slug       string             `json:"slug"`
	Path       string             `json:"path"`
	HostPath   string             `json:"hostPath"`
	HTTPPort   int                `json:"httpPort"`
	Network    string             `json:"network"`
	Containers []PreviewContainer `json:"containers"`
	Volumes    []string           `json:"volumes"`
	Images     []string           `json:"images"`
	Warnings   []string           `json:"warnings"`
	// Serves says what the primary hostname reaches: "php", "python" (application
	// server), "node" (dev server) or "static".
	Serves string `json:"serves"`
	// AppService is the application container's kind (php, python, node), empty for
	// static sites.
	AppService string `json:"appService,omitempty"`
	// DevHostname is the dev server's own host name when the Node dev server is enabled.
	DevHostname string `json:"devHostname,omitempty"`
}

// PreviewContainer summarises a planned container.
type PreviewContainer struct {
	Service string   `json:"service"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Ports   []string `json:"ports"`
	Mounts  []string `json:"mounts"`
}

// Orphan is a managed Docker resource whose project is unknown to the database.
type Orphan struct {
	Type        string    `json:"type"` // container | network | volume
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ProjectID   string    `json:"projectId"`
	ProjectName string    `json:"projectName"`
	State       string    `json:"state,omitempty"`
	Created     time.Time `json:"created,omitempty"`
}

// ReconcileReport summarises one reconciliation pass.
type ReconcileReport struct {
	At       time.Time         `json:"at"`
	Projects int               `json:"projects"`
	Orphans  []Orphan          `json:"orphans"`
	Issues   []ReconcileIssue  `json:"issues"`
	States   map[string]Status `json:"-"`
	Error    string            `json:"error,omitempty"`
}

// ReconcileIssue is a detected inconsistency between desired and actual state.
type ReconcileIssue struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Severity    string `json:"severity"` // warning | error
	Message     string `json:"message"`
}

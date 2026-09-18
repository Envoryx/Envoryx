// Package project implements the project manager: planning, lifecycle operations with
// rollback, status derivation and reconciliation between database and Docker.
package project

import (
	"errors"
	"time"

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
)

// Domain errors mapped to HTTP status codes by the API layer.
var (
	ErrNotFound      = store.ErrNotFound
	ErrConflict      = store.ErrConflict
	ErrBusy          = errors.New("project is busy with another operation")
	ErrDockerDown    = docker.ErrUnavailable
	ErrNotConfigured = errors.New("staqio is not fully configured")
)

// CreateRequest is the validated intent to create a project.
type CreateRequest struct {
	Name     string
	Path     string // relative to projects root; empty = slug
	Docroot  string // relative to the project directory
	PHP      *PHPRequest
	Node     *NodeRequest
	Database *DatabaseRequest
	Redis    *ExtraRequest
	Mailpit  *ExtraRequest
	Web      WebRequest
	Git      *GitRequest
	Env      []EnvVarRequest
	// Template scaffolds an application into the new directory (see Templates()).
	Template string
	// CreateStarter writes a starter index.php when the document root is empty.
	CreateStarter bool
	// Start starts the project right after creation.
	Start bool
}

// PHPRequest selects the PHP runtime.
type PHPRequest struct {
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

// ExtraRequest selects an auxiliary service (Redis, Mailpit).
type ExtraRequest struct {
	Version    string
	ExposePort bool
}

// ExtraUpdate adds, changes or removes an auxiliary service.
type ExtraUpdate struct {
	Enabled    bool
	Version    string
	ExposePort bool
	// RemoveData must be true to remove a service that owns a volume (Redis).
	RemoveData bool
}

// DatabaseRequest selects a database service.
type DatabaseRequest struct {
	Type       string // "mariadb"
	Version    string
	ExposePort bool // publish the database on a host port for external clients
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
	Type    string // "caddy"
	Version string
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
	PHP      *PHPRequest
	Node     *NodeUpdate
	Database *DatabaseUpdate
	Redis    *ExtraUpdate
	Mailpit  *ExtraUpdate
	Env      *[]EnvVarRequest
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
	// WebUI is the host-side URL of a web interface (Mailpit inbox), empty otherwise.
	WebUIPort int `json:"webUiPort,omitempty"`
}

// DatabaseInfo describes the database service without secrets.
type DatabaseInfo struct {
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
	Ports         []docker.PortMapping
}

// Status is the derived state of a project.
type Status struct {
	State    State           `json:"state"`
	Services []ServiceStatus `json:"services"`
	Warnings []string        `json:"warnings"`
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

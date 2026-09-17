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
	Name    string
	Path    string // relative to projects root; empty = slug
	Docroot string // relative to the project directory
	PHP     *PHPRequest
	Web     WebRequest
	Env     []EnvVarRequest
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
	Name    *string
	Docroot *string
	PHP     *PHPRequest
	Env     *[]EnvVarRequest
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

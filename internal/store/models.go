// Package store contains the SQLite repositories for Staqio's persistent state.
package store

import (
	"encoding/json"
	"time"
)

// User is a Staqio login account.
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
	ServiceWeb      ServiceKind = "web"
	ServicePHP      ServiceKind = "php"
	ServiceNode     ServiceKind = "node"
	ServiceDatabase ServiceKind = "database"
	ServiceRedis    ServiceKind = "redis"
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
	CreatedAt    time.Time
	UpdatedAt    time.Time

	Services []ProjectService
	Env      []EnvVar
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

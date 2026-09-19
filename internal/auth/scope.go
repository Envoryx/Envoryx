package auth

import (
	"errors"
	"fmt"
	"slices"
)

// Scope is the access level of an API token. Levels are ordered: every level includes
// the ones below it.
type Scope string

const (
	// ScopeRead allows looking: listing projects, status, logs, stats, backups, runtimes.
	// It reveals no secrets (no database credentials, no deploy key).
	ScopeRead Scope = "read"
	// ScopeOperate additionally allows working with existing projects: start/stop/restart,
	// running actions, creating backups and databases, git operations, domains, the
	// database browser, SSH/SFTP and the terminal.
	ScopeOperate Scope = "operate"
	// ScopeAdmin allows everything a browser session may do except managing tokens and
	// the password: creating and deleting projects, settings, TLS, instance backups,
	// image clean-up, dropping databases, restoring backups.
	ScopeAdmin Scope = "admin"
)

// Scopes lists the levels from least to most powerful.
var Scopes = []Scope{ScopeRead, ScopeOperate, ScopeAdmin}

// ErrInvalidScope is returned for unknown scope names.
var ErrInvalidScope = errors.New("scope must be read, operate or admin")

// ErrForbidden is returned when a principal's scope or project restriction does not
// cover the requested operation.
var ErrForbidden = errors.New("forbidden")

// ParseScope validates a scope name.
func ParseScope(s string) (Scope, error) {
	if slices.Contains(Scopes, Scope(s)) {
		return Scope(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidScope, s)
}

func (s Scope) rank() int {
	return slices.Index(Scopes, s) // -1 for unknown: covers nothing
}

// Covers reports whether a principal holding s may perform an operation requiring need.
func (s Scope) Covers(need Scope) bool {
	return s.rank() >= 0 && s.rank() >= need.rank()
}

// Allows reports whether the principal may perform an operation of the given level. A
// browser session always may (scopes exist for tokens only); a token principal needs a
// scope that covers the level.
func (p Principal) Allows(need Scope) bool {
	if p.TokenName == "" {
		return true
	}
	return p.Scope.Covers(need)
}

// Restricted reports whether the principal is confined to particular projects.
func (p Principal) Restricted() bool { return len(p.Projects) > 0 }

// CanAccessProject reports whether the principal may touch the project with this id.
func (p Principal) CanAccessProject(id string) bool {
	return !p.Restricted() || slices.Contains(p.Projects, id)
}

// Require returns ErrForbidden with a readable reason unless the principal covers the
// level and – when projectID is not empty – may access that project.
func (p Principal) Require(need Scope, projectID string) error {
	if !p.Allows(need) {
		return fmt.Errorf("%w: this token has %s scope, the operation needs %s", ErrForbidden, p.Scope, need)
	}
	if projectID != "" && !p.CanAccessProject(projectID) {
		return fmt.Errorf("%w: this token is limited to other projects", ErrForbidden)
	}
	return nil
}

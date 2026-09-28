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

// Role is a user's access level. The three roles match the token scopes: a viewer may
// look (read), a developer work with projects (operate), an admin everything (admin).
// RoleNone gives no access beyond the projects a user is given a role in.
type Role string

const (
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
	RoleNone      Role = "none"
)

// Roles lists the roles from most to least powerful.
var Roles = []Role{RoleAdmin, RoleDeveloper, RoleViewer, RoleNone}

// ErrInvalidRole is returned for unknown role names.
var ErrInvalidRole = errors.New("role must be admin, developer, viewer or none")

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	if slices.Contains(Roles, Role(s)) {
		return Role(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidRole, s)
}

// Scope is the token scope a role corresponds to; RoleNone (and anything unknown)
// covers nothing.
func (r Role) Scope() Scope {
	switch r {
	case RoleAdmin:
		return ScopeAdmin
	case RoleDeveloper:
		return ScopeOperate
	case RoleViewer:
		return ScopeRead
	}
	return ""
}

// globalRole is the user's role outside the project overrides. An empty role counts as
// admin: before roles existed every user was one, and principals built in code (tests,
// internal callers) carry none.
func (p Principal) globalRole() Role {
	if p.Role == "" {
		return RoleAdmin
	}
	return Role(p.Role)
}

// roleFor is the user's role in a project ("" for the instance): the project's own role
// when the user has one there, else the global role. An admin is admin everywhere.
func (p Principal) roleFor(projectID string) Role {
	global := p.globalRole()
	if global == RoleAdmin || projectID == "" {
		return global
	}
	if r, ok := p.ProjectRoles[projectID]; ok {
		return r
	}
	return global
}

// ScopeFor is what the principal may do in a project, or on the instance for "": the
// user's role there, and for an API token no more than the token's scope and never in a
// project outside the token's list. A token thus never exceeds its owner.
func (p Principal) ScopeFor(projectID string) Scope {
	s := p.roleFor(projectID).Scope()
	if p.TokenName == "" {
		return s
	}
	if projectID != "" && p.Restricted() && !slices.Contains(p.Projects, projectID) {
		return ""
	}
	if s.rank() > p.Scope.rank() {
		return p.Scope
	}
	return s
}

// MaxScope is the most the principal may do anywhere: on the instance or in any project
// it has a role in or its token names.
func (p Principal) MaxScope() Scope {
	best := p.ScopeFor("")
	consider := func(id string) {
		if s := p.ScopeFor(id); s.rank() > best.rank() {
			best = s
		}
	}
	for id := range p.ProjectRoles {
		consider(id)
	}
	for _, id := range p.Projects {
		consider(id)
	}
	return best
}

// Confined reports whether the principal only reaches particular projects: a token
// with a project list, or a user whose global role is none. Instance-wide routes are
// closed to it except the few that list or describe its own projects.
func (p Principal) Confined() bool {
	return p.Restricted() || p.globalRole() == RoleNone
}

// Allows reports whether the principal may perform an instance-wide operation of the
// given level (settings, creating projects …). A confined principal may not.
func (p Principal) Allows(need Scope) bool {
	return !p.Confined() && p.ScopeFor("").Covers(need)
}

// Restricted reports whether the principal is a token confined to particular projects.
func (p Principal) Restricted() bool { return p.TokenName != "" && len(p.Projects) > 0 }

// CanAccessProject reports whether the principal may see the project with this id.
func (p Principal) CanAccessProject(id string) bool {
	return p.ScopeFor(id).Covers(ScopeRead)
}

// Require returns ErrForbidden with a readable reason unless the principal covers the
// level, in the project when projectID isn't empty and on the instance otherwise.
func (p Principal) Require(need Scope, projectID string) error {
	if projectID == "" {
		if p.Allows(need) {
			return nil
		}
		if p.Confined() {
			return fmt.Errorf("%w: limited to particular projects", ErrForbidden)
		}
		return fmt.Errorf("%w: %s", ErrForbidden, p.lacks(need))
	}
	if !p.CanAccessProject(projectID) {
		return fmt.Errorf("%w: no access to this project", ErrForbidden)
	}
	if !p.ScopeFor(projectID).Covers(need) {
		return fmt.Errorf("%w: %s", ErrForbidden, p.lacks(need))
	}
	return nil
}

// lacks explains a missing level: the token's scope or the user's role falls short.
func (p Principal) lacks(need Scope) string {
	if p.TokenName != "" && !p.Scope.Covers(need) {
		return fmt.Sprintf("this token has %s scope, the operation needs %s", p.Scope, need)
	}
	return fmt.Sprintf("your role does not allow this (it needs %s access)", need)
}

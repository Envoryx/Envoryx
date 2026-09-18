// Package audit writes the audit log. Entries never contain secrets.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/store"
)

// Actions recorded by Staqio.
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionSetup          = "auth.setup"
	ActionPasswordChange = "auth.password_changed"

	ActionProjectCreated   = "project.created"
	ActionProjectUpdated   = "project.updated"
	ActionProjectStarted   = "project.started"
	ActionProjectStopped   = "project.stopped"
	ActionProjectRestarted = "project.restarted"
	ActionProjectDeleted   = "project.deleted"
	ActionProjectFailed    = "project.failed"

	ActionSettingsChanged = "settings.changed"
	ActionImagesPruned    = "docker.images_pruned"

	ActionBackupCreated  = "backup.created"
	ActionBackupRestored = "backup.restored"
	ActionBackupDeleted  = "backup.deleted"

	ActionDBCredentialsViewed = "database.credentials_viewed"
	ActionDBPasswordRotated   = "database.password_rotated"
	ActionDBCreated           = "database.created"
	ActionDBDropped           = "database.dropped"

	ActionTerminalOpened = "terminal.opened"
	ActionRun            = "action.run"

	ActionDeployKeyGenerated = "git.deploy_key_generated"
	ActionGitClone           = "git.clone"
	ActionGitPull            = "git.pull"
	ActionGitCheckout        = "git.checkout"
)

type ipKey struct{}

// WithClientIP stores the request IP in the context so audit entries can record it.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipKey{}, ip)
}

// Logger appends audit entries.
type Logger struct {
	store *store.Audit
	log   *slog.Logger
}

// New creates an audit logger.
func New(st *store.Audit, log *slog.Logger) *Logger {
	return &Logger{store: st, log: log}
}

// Log records an action performed by the principal in ctx (if any). details is
// marshalled to JSON; pass nil for none. Failures to write the audit log are logged but
// do not fail the business operation.
func (l *Logger) Log(ctx context.Context, action, targetType, targetID string, details any) {
	e := store.AuditEntry{Action: action, TargetType: targetType, TargetID: targetID}
	if p, ok := auth.PrincipalFrom(ctx); ok {
		e.UserID, e.Username = p.UserID, p.Username
	}
	if ip, ok := ctx.Value(ipKey{}).(string); ok {
		e.IP = ip
	}
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			l.log.Warn("audit details not serialisable", "action", action, "err", err)
		} else {
			e.Details = b
		}
	}
	// Audit writes must succeed even when the request context is already cancelled.
	if err := l.store.Append(context.WithoutCancel(ctx), e); err != nil {
		l.log.Error("audit log write failed", "action", action, "err", err)
	}
	l.log.Info("audit", "action", action, "target", targetType, "id", targetID, "user", e.Username)
}

// LogAs records an action on behalf of an explicit username (e.g. failed logins).
func (l *Logger) LogAs(ctx context.Context, username, action, targetType, targetID string, details any) {
	e := store.AuditEntry{Username: username, Action: action, TargetType: targetType, TargetID: targetID}
	if ip, ok := ctx.Value(ipKey{}).(string); ok {
		e.IP = ip
	}
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			e.Details = b
		}
	}
	if err := l.store.Append(context.WithoutCancel(ctx), e); err != nil {
		l.log.Error("audit log write failed", "action", action, "err", err)
	}
}

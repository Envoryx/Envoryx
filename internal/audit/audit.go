// Package audit writes the audit log. Entries never contain secrets.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

// Actions recorded by Envoryx.
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionSetup          = "auth.setup"
	ActionPasswordChange = "auth.password_changed"
	// Rescue CLI (envoryx admin …), run from a shell inside the container.
	ActionPasswordReset   = "auth.password_reset"
	ActionSessionsRevoked = "auth.sessions_revoked"
	ActionTokensRevoked   = "auth.tokens_revoked"
	ActionAccountsReset   = "auth.accounts_reset"

	ActionProjectCreated    = "project.created"
	ActionProjectUpdated    = "project.updated"
	ActionProjectStarted    = "project.started"
	ActionProjectStopped    = "project.stopped"
	ActionProjectRestarted  = "project.restarted"
	ActionProjectDuplicated = "project.duplicated"
	ActionProjectRenamed    = "project.renamed"
	ActionProjectDeleted    = "project.deleted"
	ActionProjectFailed     = "project.failed"

	ActionSettingsChanged = "settings.changed"
	ActionDBToolOpened    = "project.dbtool_opened"
	ActionImageRolledBack = "project.image_rolled_back"
	ActionImageLatest     = "project.image_latest"
	ActionImagesPruned    = "docker.images_pruned"
	ActionOrphansRemoved  = "docker.orphans_removed"

	ActionBackupCreated  = "backup.created"
	ActionBackupRestored = "backup.restored"
	ActionBackupDeleted  = "backup.deleted"

	ActionInstanceBackupCreated  = "instance.backup_created"
	ActionInstanceBackupUploaded = "instance.backup_uploaded"
	ActionInstanceBackupDeleted  = "instance.backup_deleted"
	ActionInstanceRestore        = "instance.restore_scheduled"
	// ActionInstanceRestored is written after a restart applied a scheduled restore. The
	// restore_scheduled entry lives in the database that the restore replaced, so this is
	// the only trace that survives in the restored database.
	ActionInstanceRestored = "instance.restored"

	ActionDBCredentialsViewed = "database.credentials_viewed"
	ActionDBPasswordRotated   = "database.password_rotated"
	ActionDBCreated           = "database.created"
	ActionDBDropped           = "database.dropped"

	ActionTerminalOpened = "terminal.opened"
	ActionExec           = "project.exec"
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

// Actor is the user name an audit row shows for a principal: the account, suffixed with
// the API token's name when the request was not a browser session.
func Actor(p auth.Principal) string {
	if p.TokenName != "" {
		return p.Username + " (token: " + p.TokenName + ")"
	}
	return p.Username
}

// Log records an action performed by the principal in ctx (if any). details is
// marshalled to JSON; pass nil for none. Failures to write the audit log are logged but
// do not fail the business operation.
func (l *Logger) Log(ctx context.Context, action, targetType, targetID string, details any) {
	e := store.AuditEntry{Action: action, TargetType: targetType, TargetID: targetID}
	if p, ok := auth.PrincipalFrom(ctx); ok {
		e.UserID, e.Username = p.UserID, Actor(p)
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

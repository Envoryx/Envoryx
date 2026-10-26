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
	ActionLogin       = "auth.login"
	ActionLoginFailed = "auth.login_failed"
	ActionLogout      = "auth.logout"
	// ActionSupportLogin is the hoster's support signing in through the fleet manager;
	// ActionSupportAccess a change of the admins' consent to it.
	ActionSupportLogin   = "auth.support_login"
	ActionSupportAccess  = "settings.support_access"
	ActionSetup          = "auth.setup"
	ActionPasswordChange = "auth.password_changed"
	// Two-factor authentication: the own factors, and an admin (or the rescue CLI)
	// resetting a user's.
	ActionTOTPEnabled          = "auth.totp_enabled"
	ActionTOTPDisabled         = "auth.totp_disabled"
	ActionPasskeyAdded         = "auth.passkey_added"
	ActionPasskeyRemoved       = "auth.passkey_removed"
	ActionRecoveryCodesRenewed = "auth.recovery_codes_renewed"
	ActionRecoveryCodeUsed     = "auth.recovery_code_used"
	ActionTwoFactorReset       = "auth.two_factor_reset"
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
	ActionWorkerRestarted   = "project.worker_restarted"
	ActionProjectDuplicated = "project.duplicated"
	ActionProjectRenamed    = "project.renamed"
	// ActionProjectShared is written when a project goes out on a public tunnel address,
	// ActionProjectUnshared when that ends, by hand or with the reason in its details.
	ActionProjectShared   = "project.shared"
	ActionProjectUnshared = "project.unshared"
	ActionProjectDeleted  = "project.deleted"
	ActionProjectFailed   = "project.failed"

	ActionSettingsChanged = "settings.changed"
	ActionDBToolOpened    = "project.dbtool_opened"
	ActionImageRolledBack = "project.image_rolled_back"
	ActionImageLatest     = "project.image_latest"
	ActionImagesPruned    = "docker.images_pruned"
	// ActionCustomImageChanged is written when a runtime gets a custom image, returns to
	// the catalogue image or its Dockerfile is rebuilt.
	ActionCustomImageChanged = "project.custom_image_changed"
	// ActionAddonInstalled is written when an addon file is installed or replaced,
	// ActionAddonRemoved when one is deleted.
	// ActionSecretKeyRevealed is written when an admin shows the secret key,
	// ActionSecretKeyRotated when a new key replaces it.
	ActionSecretKeyRevealed = "settings.secret_key_revealed"
	ActionSecretKeyRotated  = "settings.secret_key_rotated"
	ActionAddonInstalled    = "addon.installed"
	ActionAddonRemoved      = "addon.removed"
	// ActionRegistriesChanged is written when the private registry logins change.
	ActionRegistriesChanged = "settings.registries_changed"
	ActionOrphansRemoved    = "docker.orphans_removed"
	// ActionLogHistoryCleared is written when the stored log history is deleted.
	ActionLogHistoryCleared = "logs.history_cleared"
	// ActionPackageCacheCleared is written when the shared package cache, or one tool's
	// part of it, is emptied.
	ActionPackageCacheCleared = "cache.cleared"
	// ActionOllamaModelPulled is written when a model download into the shared Ollama
	// store ends, successful or not (its status says which). ActionOllamaModelDeleted is
	// written when a model is removed from the store.
	ActionOllamaModelPulled  = "ollama.model_pulled"
	ActionOllamaModelDeleted = "ollama.model_deleted"

	ActionBackupCreated = "backup.created"
	// ActionBackupImported is a backup that came from elsewhere: uploaded, or fetched
	// from an offsite target.
	ActionBackupImported = "backup.imported"
	ActionBackupRestored = "backup.restored"
	// ActionBackupRestoredNew is a backup restored into a new project.
	ActionBackupRestoredNew = "backup.restored_new"
	ActionBackupDeleted     = "backup.deleted"
	// ActionBackupFailed is written for every backup that could not be created, manual or
	// scheduled.
	ActionBackupFailed = "backup.failed"

	ActionInstanceBackupCreated  = "instance.backup_created"
	ActionInstanceBackupUploaded = "instance.backup_uploaded"
	ActionInstanceBackupDeleted  = "instance.backup_deleted"
	ActionInstanceRestore        = "instance.restore_scheduled"
	// ActionInstanceRestored is written after a restart applied a scheduled restore. The
	// restore_scheduled entry lives in the database that the restore replaced, so this is
	// the only trace that survives in the restored database.
	ActionInstanceRestored = "instance.restored"
	// ActionInstanceSuspended and ActionInstanceUnsuspended record the hoster's suspension
	// of a managed instance and its end.
	ActionInstanceSuspended   = "instance.suspended"
	ActionInstanceUnsuspended = "instance.unsuspended"
	// ActionInstanceUpdated and ActionInstanceUpdateFailed record a self-update on the
	// hoster's request, written by the version that runs afterwards.
	ActionInstanceUpdated      = "instance.updated"
	ActionInstanceUpdateFailed = "instance.update_failed"

	ActionDBCredentialsViewed = "database.credentials_viewed"
	ActionDBPasswordRotated   = "database.password_rotated"
	ActionDBCreated           = "database.created"
	ActionDBDropped           = "database.dropped"
	ActionDBCloned            = "database.cloned"

	ActionTerminalOpened = "terminal.opened"
	ActionExec           = "project.exec"
	ActionRun            = "action.run"
	ActionTestRun        = "test.run"
	ActionCronRun        = "cron.run"

	ActionDeployKeyGenerated = "git.deploy_key_generated"
	ActionGitClone           = "git.clone"
	ActionGitPull            = "git.pull"
	ActionGitCheckout        = "git.checkout"
	ActionBranchDeployed     = "branch.deployed"

	ActionUserInvited = "user.invited"
	ActionUserJoined  = "user.joined"
	ActionUserUpdated = "user.updated"
	ActionUserDeleted = "user.deleted"
	// ActionUserSSOCreated is a user single sign-on created at its first sign-in.
	ActionUserSSOCreated = "user.sso_created"
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
// marshalled to JSON; pass nil for none. If the audit log can't be written, that's
// logged, but the action itself still succeeds.
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

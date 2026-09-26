package project

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/validate"
)

// SettingAuditDays is how many days the audit log keeps its entries; 0 keeps them all.
const (
	SettingAuditDays = "audit_retention_days"
	MaxAuditDays     = 3650
)

// AuditSettings are the audit log's settings.
type AuditSettings struct {
	// RetentionDays drops entries older than this; 0 keeps every entry.
	RetentionDays int `json:"retentionDays"`
}

// AuditSettings returns how long the audit log keeps its entries.
func (m *Manager) AuditSettings(ctx context.Context) AuditSettings {
	return AuditSettings{RetentionDays: m.intSetting(ctx, SettingAuditDays, 0, 0, MaxAuditDays)}
}

// SetAuditRetention sets how many days the audit log keeps (0 = all) and applies it now.
func (m *Manager) SetAuditRetention(ctx context.Context, days int) (AuditSettings, error) {
	if days < 0 || days > MaxAuditDays {
		return AuditSettings{}, fmt.Errorf("%w: keep the audit log for 1 to %d days, or 0 for all of it", validate.ErrInvalid, MaxAuditDays)
	}
	if err := m.store.Settings.Set(ctx, SettingAuditDays, strconv.Itoa(days)); err != nil {
		return AuditSettings{}, err
	}
	// Logged before the prune, so the change itself is the newest entry that survives it.
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"auditRetentionDays": days})
	if _, err := m.pruneAudit(ctx); err != nil {
		return AuditSettings{}, err
	}
	return m.AuditSettings(ctx), nil
}

// pruneAudit drops the entries retention no longer keeps.
func (m *Manager) pruneAudit(ctx context.Context) (int64, error) {
	days := m.AuditSettings(ctx).RetentionDays
	if days == 0 {
		return 0, nil
	}
	return m.store.Audit.Prune(ctx, time.Now().AddDate(0, 0, -days))
}

// RunAuditRetention applies the audit log's retention every interval until ctx ends.
func (m *Manager) RunAuditRetention(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := m.pruneAudit(ctx); err != nil {
			log.Warn("audit log retention failed", "err", err)
		} else if n > 0 {
			log.Info("audit log entries past retention removed", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

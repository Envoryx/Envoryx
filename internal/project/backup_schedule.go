package project

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

// SetBackupSchedule validates and stores a project's backup schedule.
func (m *Manager) SetBackupSchedule(ctx context.Context, id string, b store.BackupSchedule) (store.BackupSchedule, error) {
	if err := validate.UUID(id); err != nil {
		return store.BackupSchedule{}, ErrNotFound
	}
	switch b.Schedule {
	case "", "daily", "weekly":
	default:
		return store.BackupSchedule{}, fmt.Errorf("%w: schedule must be daily, weekly or empty", validate.ErrInvalid)
	}
	if b.Hour < 0 || b.Hour > 23 {
		return store.BackupSchedule{}, fmt.Errorf("%w: hour must be 0-23", validate.ErrInvalid)
	}
	if b.Weekday < 0 || b.Weekday > 6 {
		return store.BackupSchedule{}, fmt.Errorf("%w: weekday must be 0 (Sunday) to 6", validate.ErrInvalid)
	}
	if b.Keep < 1 || b.Keep > 365 {
		return store.BackupSchedule{}, fmt.Errorf("%w: keep must be 1-365", validate.ErrInvalid)
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return store.BackupSchedule{}, err
	}
	if err := m.store.Projects.UpdateBackupSchedule(ctx, id, b); err != nil {
		return store.BackupSchedule{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"backupSchedule": b.Schedule, "keep": b.Keep}})
	b.LastRun = p.Backup.LastRun
	return b, nil
}

// backupDue returns the most recent scheduled run time that is not after now, or the zero
// time when the schedule is off.
func backupDue(b store.BackupSchedule, now time.Time) time.Time {
	if b.Schedule == "" {
		return time.Time{}
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), b.Hour, 0, 0, 0, now.Location())
	if due.After(now) {
		due = due.AddDate(0, 0, -1)
	}
	if b.Schedule == "weekly" {
		for due.Weekday() != time.Weekday(b.Weekday) {
			due = due.AddDate(0, 0, -1)
		}
	}
	return due
}

// RunBackupScheduler creates scheduled backups and applies retention until ctx ends.
func (m *Manager) RunBackupScheduler(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runScheduledBackups(ctx, time.Now(), log)
		}
	}
}

// runScheduledBackups performs one scheduler pass.
func (m *Manager) runScheduledBackups(ctx context.Context, now time.Time, log *slog.Logger) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		log.Warn("backup scheduler: list projects", "err", err)
		return
	}
	for _, p := range projects {
		due := backupDue(p.Backup, now)
		if due.IsZero() || !p.Backup.LastRun.Before(due) || p.Lifecycle != store.LifecycleReady {
			continue
		}
		// Record the run first so a failing backup is retried at the next slot, not every minute.
		if err := m.store.Projects.SetBackupLastRun(ctx, p.ID, now); err != nil {
			log.Warn("backup scheduler: record run", "project", p.Slug, "err", err)
			continue
		}
		info, err := m.CreateBackup(ctx, p.ID, BackupOptions{Database: true, Files: true, IncludeDependencies: p.Backup.IncludeDependencies, Note: "scheduled " + p.Backup.Schedule, Source: "scheduled"})
		if err != nil {
			log.Warn("scheduled backup failed", "project", p.Slug, "err", err)
			continue
		}
		log.Info("scheduled backup created", "project", p.Slug, "backup", info.ID, "bytes", info.SizeBytes)
		m.applyRetention(ctx, p, log)
	}
}

// applyRetention deletes the oldest scheduled backups beyond the project's keep count.
// Manual backups are never touched.
func (m *Manager) applyRetention(ctx context.Context, p store.Project, log *slog.Logger) {
	list, err := m.ListBackups(ctx, p.ID)
	if err != nil {
		log.Warn("backup retention: list", "project", p.Slug, "err", err)
		return
	}
	var scheduled []BackupInfo
	for _, b := range list {
		if b.Meta.Source == "scheduled" {
			scheduled = append(scheduled, b)
		}
	}
	sort.Slice(scheduled, func(i, j int) bool { return scheduled[i].CreatedAt.After(scheduled[j].CreatedAt) })
	for i := p.Backup.Keep; i < len(scheduled); i++ {
		if err := m.DeleteBackup(ctx, p.ID, scheduled[i].ID); err != nil {
			log.Warn("backup retention: delete", "project", p.Slug, "backup", scheduled[i].ID, "err", err)
			continue
		}
		log.Info("old scheduled backup removed", "project", p.Slug, "backup", scheduled[i].ID)
	}
}

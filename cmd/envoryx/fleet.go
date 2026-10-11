package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/fleet"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/store"
)

// remoteBackups takes the instance backups a hoster's fleet manager asks for, copies
// them to the hoster's offsite target and tells the manager about the instance backups.
type remoteBackups struct {
	backups *instance.Store
	db      *sql.DB
	st      *store.Store
	// syncer is nil when offsite targets are unavailable.
	syncer *offsite.Syncer
	log    *slog.Logger
	// done is called after a backup finished (the agent reports at once).
	done func()

	mu      sync.Mutex
	running bool
	lastErr string
}

// Start takes an instance backup in the background.
func (b *remoteBackups) Start(context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return errors.New("an instance backup is being taken already")
	}
	b.running = true
	b.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		info, err := b.backups.Create(ctx, b.db, instance.KindManual, "requested by the hoster")
		if err == nil && b.syncer != nil {
			if _, gerr := b.syncer.Config.Get(offsite.ManagedID); gerr == nil {
				if _, uerr := b.syncer.UploadInstance(ctx, info.ID, []string{offsite.ManagedID}); uerr != nil {
					b.log.Warn("fleet: queue the instance backup for the hoster's target", "err", uerr)
				}
			}
		}
		b.mu.Lock()
		b.running, b.lastErr = false, ""
		if err != nil {
			b.lastErr = err.Error()
		}
		b.mu.Unlock()
		if err != nil {
			b.log.Warn("fleet: instance backup failed", "err", err)
		} else {
			b.log.Info("fleet: instance backup taken", "backup", info.ID)
		}
		if b.done != nil {
			b.done()
		}
	}()
	return nil
}

// Status lists the newest instance backups and how far the hoster's target got.
func (b *remoteBackups) Status(ctx context.Context) *fleet.BackupStatus {
	out := &fleet.BackupStatus{Latest: []fleet.InstanceBackup{}}
	b.mu.Lock()
	out.Running, out.LastError = b.running, b.lastErr
	b.mu.Unlock()
	list, err := b.backups.List()
	if err != nil {
		out.LastError = err.Error()
		return out
	}
	if len(list) > fleet.MaxReportedBackups {
		list = list[:fleet.MaxReportedBackups]
	}
	ids := make([]string, 0, len(list))
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	copied := map[string]bool{}
	var lastFail time.Time
	if uploads, err := b.st.Offsite.ByBackups(ctx, store.OffsiteInstance, ids); err == nil {
		for _, u := range uploads {
			if u.TargetID != offsite.ManagedID {
				continue
			}
			switch u.Status {
			case store.OffsiteDone:
				copied[u.BackupID] = true
			case store.OffsiteFailed:
				if u.UpdatedAt.After(lastFail) {
					lastFail, out.OffsiteError = u.UpdatedAt, u.Error
				}
			}
		}
	}
	for _, i := range list {
		out.Latest = append(out.Latest, fleet.InstanceBackup{ID: i.ID, Kind: i.Kind, SizeBytes: i.SizeBytes, CreatedAt: i.CreatedAt, Offsite: copied[i.ID]})
		if copied[i.ID] && i.CreatedAt.After(out.OffsiteAt) {
			out.OffsiteAt = i.CreatedAt
		}
	}
	// A failure older than the newest copy is history.
	if !out.OffsiteAt.IsZero() && lastFail.Before(out.OffsiteAt) {
		out.OffsiteError = ""
	}
	return out
}

package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A snapshot is a backup of one database of a project and nothing else: the dump you want
// taken before a migration, a mass update or a query you are not sure about, and put back
// with one click when it goes wrong. It is the ordinary backup machinery – same directory
// under /config/backups, same metadata, same dump and import path – so a snapshot can be
// listed, downloaded, restored and deleted like any other backup; what sets it apart is
// that it holds the database only and carries `source: "snapshot"`.
//
// Snapshots roll: the newest snapshotKeep of a project stay, older ones go when a new one
// is taken. Taking one is meant to be cheap enough to do before every migration, and a
// year of those is not what anybody wants to find in their backup directory. Only
// snapshots are pruned – scheduled backups, the dump a database upgrade takes and
// everything made by hand are left alone.
const (
	snapshotSource = "snapshot"
	snapshotKeep   = 10
)

// CreateSnapshot dumps a database of a project (db "" = the primary). Unlike a backup it
// does not need the project to be running: a stopped database container is started for
// the dump and stopped again afterwards.
func (m *Manager) CreateSnapshot(ctx context.Context, id, db, note string) (BackupInfo, error) {
	if err := validate.UUID(id); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	if len(note) > 500 {
		return BackupInfo{}, fmt.Errorf("%w: note too long", validate.ErrInvalid)
	}
	var info BackupInfo
	err := m.run(ctx, limitBackup, Operation{Action: "snapshot", ProjectID: id}, func(ctx context.Context) error {
		unlock, err := m.lock(id)
		if err != nil {
			return err
		}
		defer unlock()
		p, err := m.loadProject(ctx, id)
		if err != nil {
			return err
		}
		info, err = m.snapshotLocked(ctx, p, db, note, snapshotSource)
		return err
	})
	if err != nil {
		return BackupInfo{}, err
	}
	m.pruneSnapshots(context.WithoutCancel(ctx), id, db)
	return info, nil
}

// snapshotLocked dumps one database for a caller that already holds the project lock.
// source says who asked: a snapshot taken by hand, or a clone protecting the state it is
// about to overwrite.
func (m *Manager) snapshotLocked(ctx context.Context, p store.Project, db, note, source string) (BackupInfo, error) {
	svc, cfg, err := databaseOf(p, db)
	if err != nil {
		return BackupInfo{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return BackupInfo{}, err
	}
	var info BackupInfo
	err = m.withServiceRunning(ctx, p, svc.Kind, func(ctx context.Context) error {
		if err := m.waitForDatabase(ctx, p, svc, cfg, dialect); err != nil {
			return err
		}
		b, err := m.createBackupLocked(ctx, p, BackupOptions{Database: true, OnlyDB: &db, Note: note, Source: source})
		if err != nil {
			return err
		}
		info = b
		return nil
	})
	return info, err
}

// ListSnapshots returns the backups of a project that hold a dump of the database db and
// nothing but dumps, newest first – the snapshots taken by hand, the ones a clone took and
// the dump a database upgrade insisted on.
func (m *Manager) ListSnapshots(ctx context.Context, id, db string) ([]BackupInfo, error) {
	list, err := m.ListBackups(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]BackupInfo, 0, len(list))
	for _, b := range list {
		if b.Kind == "database" && b.Meta.HasDatabase(db) {
			out = append(out, b)
		}
	}
	return out, nil
}

// RestoreSnapshot imports a snapshot's dump back into the project's database and touches
// nothing else. Destructive: everything written since the snapshot is gone, so confirm
// must be the project's identifier. A stopped database container is started for the
// import and stopped again afterwards.
func (m *Manager) RestoreSnapshot(ctx context.Context, id, db, snapshotID, confirm string) (BackupInfo, error) {
	if err := validate.UUID(id); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	if err := validate.UUID(snapshotID); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	var info BackupInfo
	err := m.run(ctx, limitBackup, Operation{Action: "restore", ProjectID: id}, func(ctx context.Context) error {
		unlock, err := m.lock(id)
		if err != nil {
			return err
		}
		defer unlock()
		p, err := m.loadProject(ctx, id)
		if err != nil {
			return err
		}
		if confirm != p.Slug {
			return fmt.Errorf("%w: confirmation must equal the project identifier %q", validate.ErrInvalid, p.Slug)
		}
		svc, cfg, err := databaseOf(p, db)
		if err != nil {
			return err
		}
		dialect, err := dialectOf(svc)
		if err != nil {
			return err
		}
		b, err := m.store.Backups.Get(ctx, id, snapshotID)
		if err != nil {
			return err
		}
		var meta BackupMeta
		_ = json.Unmarshal(b.Metadata, &meta)
		dump, ok := meta.dump(db)
		if !ok {
			return fmt.Errorf("%w: this snapshot holds no dump of %s", validate.ErrInvalid, dbLabel(db))
		}
		if err := checkDumpType(dump.Type, svc); err != nil {
			return err
		}
		dir, err := m.backupDir(p.Slug, b.Filename)
		if err != nil {
			return err
		}
		err = m.withServiceRunning(ctx, p, svc.Kind, func(ctx context.Context) error {
			if err := m.waitForDatabase(ctx, p, svc, cfg, dialect); err != nil {
				return err
			}
			_, _, err := m.restoreDatabases(ctx, p, meta, dir, &db)
			return err
		})
		if err != nil {
			return err
		}
		m.audit.Log(ctx, audit.ActionBackupRestored, "project", id, auditDB(map[string]any{"name": p.Name, "backup": snapshotID, "database": true, "snapshot": true}, db))
		info = BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}
		return nil
	})
	return info, err
}

// pruneSnapshots keeps the newest snapshotKeep snapshots of a database and deletes the
// rest. Failures are logged and otherwise ignored: the snapshot that was just taken is
// what the caller asked for, and an old one left behind is no reason to fail it.
func (m *Manager) pruneSnapshots(ctx context.Context, id, db string) {
	list, err := m.ListBackups(ctx, id)
	if err != nil {
		return
	}
	var snapshots []BackupInfo
	for _, b := range list {
		if b.Meta.Source == snapshotSource && snapshotOf(b.Meta) == db {
			snapshots = append(snapshots, b)
		}
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt) })
	for i := snapshotKeep; i < len(snapshots); i++ {
		if err := m.DeleteBackup(ctx, id, snapshots[i].ID); err != nil {
			m.log.Warn("old snapshot not removed", "project", id, "snapshot", snapshots[i].ID, "err", err)
			continue
		}
		m.log.Info("old snapshot removed", "project", id, "snapshot", snapshots[i].ID)
	}
}

// snapshotOf names the database a snapshot was taken of (each holds exactly one dump).
func snapshotOf(meta BackupMeta) string {
	if meta.Database == nil && len(meta.Databases) == 1 {
		return meta.Databases[0].DB
	}
	return ""
}

// CloneDatabaseRequest is the intent to replace one project's database contents with
// another's.
type CloneDatabaseRequest struct {
	// Source is the project whose database is copied.
	Source string
	// DB is the database of this project that is replaced ("" = the primary).
	DB string
	// SourceDB is the database of the source that is copied (nil = the one named like DB).
	SourceDB *string
	// Snapshot takes a snapshot of the target's database first, so what the clone
	// overwrites can be put back.
	Snapshot bool
	// Confirm must equal the target project's identifier.
	Confirm string
}

// CloneDatabaseResult reports what a clone did.
type CloneDatabaseResult struct {
	// Source is the identifier of the project the data came from, Database the name of
	// the database it went into.
	Source   string `json:"source"`
	Database string `json:"database"`
	// Snapshot is the snapshot taken of the target beforehand, if one was.
	Snapshot *BackupInfo `json:"snapshot,omitempty"`
}

// CloneDatabase copies the contents of another project's primary database into this one –
// "give me what staging has" without a dump file in between: the dump is piped straight
// into the target's client, the same way a duplicated project gets its data. The source is
// only read; the target's database is replaced, so a snapshot of it is taken first unless
// the caller says otherwise, and the target's identifier has to be confirmed.
func (m *Manager) CloneDatabase(ctx context.Context, id string, req CloneDatabaseRequest) (CloneDatabaseResult, error) {
	if err := validate.UUID(id); err != nil {
		return CloneDatabaseResult{}, ErrNotFound
	}
	if err := validate.UUID(req.Source); err != nil {
		return CloneDatabaseResult{}, fmt.Errorf("%w: which project should the data come from?", validate.ErrInvalid)
	}
	sourceDB := req.DB
	if req.SourceDB != nil {
		sourceDB = *req.SourceDB
	}
	if req.Source == id && sourceDB == req.DB {
		return CloneDatabaseResult{}, fmt.Errorf("%w: source and target are the same database", validate.ErrInvalid)
	}
	var res CloneDatabaseResult
	err := m.run(ctx, limitDuplicate, Operation{Action: "clone-database", ProjectID: id}, func(ctx context.Context) (err error) {
		res, err = m.cloneDatabase(ctx, id, req)
		return err
	})
	if res.Snapshot != nil {
		m.pruneSnapshots(context.WithoutCancel(ctx), id, req.DB)
	}
	return res, err
}

func (m *Manager) cloneDatabase(ctx context.Context, id string, req CloneDatabaseRequest) (CloneDatabaseResult, error) {
	sourceDB := req.DB
	if req.SourceDB != nil {
		sourceDB = *req.SourceDB
	}
	// The source is read for the length of the clone and the target rewritten, so both
	// are locked, source first as when a project is duplicated – once, when they are two
	// databases of the same project.
	if req.Source != id {
		unlockSrc, err := m.lock(req.Source)
		if err != nil {
			return CloneDatabaseResult{}, err
		}
		defer unlockSrc()
	}
	unlock, err := m.lock(id)
	if err != nil {
		return CloneDatabaseResult{}, err
	}
	defer unlock()
	src, err := m.loadProject(ctx, req.Source)
	if err != nil {
		return CloneDatabaseResult{}, fmt.Errorf("source project: %w", err)
	}
	dst, err := m.loadProject(ctx, id)
	if err != nil {
		return CloneDatabaseResult{}, err
	}
	if req.Confirm != dst.Slug {
		return CloneDatabaseResult{}, fmt.Errorf("%w: confirmation must equal the project identifier %q", validate.ErrInvalid, dst.Slug)
	}
	for _, p := range []store.Project{src, dst} {
		if p.Lifecycle != store.LifecycleReady {
			return CloneDatabaseResult{}, fmt.Errorf("%w: %s is %s", ErrConflict, p.Name, p.Lifecycle)
		}
	}
	srcSvc, srcCfg, err := databaseOf(src, sourceDB)
	if errors.Is(err, store.ErrNotFound) {
		return CloneDatabaseResult{}, fmt.Errorf("%w: %s has no database %s to copy", validate.ErrInvalid, src.Slug, dbLabel(sourceDB))
	}
	if err != nil {
		return CloneDatabaseResult{}, err
	}
	dstSvc, dstCfg, err := databaseOf(dst, req.DB)
	if err != nil {
		return CloneDatabaseResult{}, err
	}
	if srcSvc.Variant != dstSvc.Variant {
		return CloneDatabaseResult{}, fmt.Errorf("%w: %s runs %s, %s runs %s – a dump of one is not a dump of the other", ErrConflict, src.Slug, srcSvc.Variant, dst.Slug, dstSvc.Variant)
	}
	res := CloneDatabaseResult{Source: src.Slug, Database: dstCfg.Database}
	// One start of the target's database covers the snapshot and the import; copyDatabase
	// finds it running and leaves it as it is.
	err = m.withServiceRunning(ctx, dst, dstSvc.Kind, func(ctx context.Context) error {
		if req.Snapshot {
			snapshot, err := m.snapshotLocked(ctx, dst, req.DB, "before cloning the database of "+src.Slug, snapshotSource)
			if err != nil {
				return fmt.Errorf("clone refused: the database of %s could not be snapshotted first (%w); take the snapshot out of the request to clone anyway", dst.Slug, err)
			}
			res.Snapshot = &snapshot
		}
		step(ctx, "Copying the database of {{project}}", "project", src.Slug)
		return m.copyDatabaseOf(ctx, src, sourceDB, dst, req.DB)
	})
	if err != nil {
		return res, err
	}
	details := auditDB(map[string]any{
		"name": dst.Name, "database": dstCfg.Database, "source": src.Slug, "sourceDatabase": srcCfg.Database, "snapshot": res.Snapshot != nil,
	}, req.DB)
	if sourceDB != "" {
		details["sourceDb"] = sourceDB
	}
	m.audit.Log(ctx, audit.ActionDBCloned, "project", id, details)
	return res, nil
}

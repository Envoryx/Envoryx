package project

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Backup layout under /config/backups/<slug>/<dir>/:
//
//	backup.json      metadata + project export (services, env, credentials)
//	database.sql.gz  logical dump of the primary database (optional)
//	database-<name>.sql.gz  dump of an additional database (optional, one each)
//	files.tar.gz     project directory (optional)
//	storage.tar.gz   objects of the project's bucket (optional)
const (
	backupMetaFile  = "backup.json"
	backupDBFile    = "database.sql.gz"
	backupFilesFile = "files.tar.gz"
	backupFormat    = 1
)

// BackupOptions select what a backup contains.
type BackupOptions struct {
	Database bool
	// OnlyDB limits the dump to one database ("" for all of them); set by snapshots, which
	// are taken of one database at a time. Primary is its name for the primary.
	OnlyDB *string
	Files  bool
	// Storage includes the object storage bucket (ignored when the project has none,
	// unless it is the only thing requested).
	Storage bool
	// IncludeDependencies keeps vendor/ and node_modules/ in the file archive.
	IncludeDependencies bool
	Note                string
	// Source marks who created the backup ("manual" default, "scheduled", "upgrade").
	Source string
}

// RestoreOptions select what to restore. Confirm must equal the project slug.
type RestoreOptions struct {
	Database bool
	Files    bool
	Storage  bool
	// WipeFiles empties the project directory before extracting; WipeStorage empties the
	// bucket before uploading.
	WipeFiles   bool
	WipeStorage bool
	// FlushRedis empties the project's Redis once the database or the files are back:
	// sessions, caches and queued jobs written since the backup point at data that is gone
	// (WordPress' object cache, Shopware's HTTP cache, Laravel sessions). Only Redis that
	// Envoryx runs; an external one is left alone.
	FlushRedis bool
	Confirm    string
}

// BackupMeta is written to backup.json and returned by the API (without the export).
type BackupMeta struct {
	Format      int       `json:"format"`
	Envoryx     string    `json:"envoryx"`
	ProjectID   string    `json:"projectId"`
	ProjectName string    `json:"projectName"`
	Slug        string    `json:"slug"`
	CreatedAt   time.Time `json:"createdAt"`
	Note        string    `json:"note,omitempty"`
	Source      string    `json:"source,omitempty"`
	// Database is the dump of the primary database.
	Database *DumpMeta `json:"database,omitempty"`
	// Databases are the dumps of additional databases.
	Databases []ExtraDumpMeta `json:"databases,omitempty"`
	Files     *struct {
		Bytes               int64 `json:"bytes"`
		Entries             int   `json:"entries"`
		IncludeDependencies bool  `json:"includeDependencies"`
	} `json:"files,omitempty"`
	Storage *struct {
		Bucket  string `json:"bucket"`
		Objects int    `json:"objects"`
		Bytes   int64  `json:"bytes"`
	} `json:"storage,omitempty"`
	Runtimes map[string]string `json:"runtimes"`
	// AddonVolumes are the archives of addon volumes (addon-<name>-<volume>.tar.gz).
	AddonVolumes []string `json:"addonVolumes,omitempty"`
	// DatabaseVolumes are copies of database data volumes, taken instead of a dump for a
	// database whose server cannot start on the host (see createBackupLocked).
	DatabaseVolumes []VolumeCopyMeta `json:"databaseVolumes,omitempty"`
}

// VolumeCopyMeta describes the file-level copy of a database's data volume in a backup.
type VolumeCopyMeta struct {
	// DB is the database's name in the project, "" for the primary.
	DB      string `json:"db,omitempty"`
	Type    string `json:"type"`
	Version string `json:"version"`
	File    string `json:"file"`
	Bytes   int64  `json:"bytes"`
}

// DumpMeta describes a database dump in a backup.
type DumpMeta struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	// Name is the database on the server (cfg.Database).
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// ExtraDumpMeta is the dump of an additional database; DB is its name in the project.
type ExtraDumpMeta struct {
	DB string `json:"db"`
	DumpMeta
}

// HasDatabase reports whether the backup holds a dump of the database named db ("" for
// the primary).
func (b BackupMeta) HasDatabase(db string) bool {
	_, ok := b.dump(db)
	return ok
}

// HasAnyDatabase reports whether the backup holds any database, as a dump or as a copy
// of the data volume.
func (b BackupMeta) HasAnyDatabase() bool {
	return b.Database != nil || len(b.Databases) > 0 || len(b.DatabaseVolumes) > 0
}

func (b BackupMeta) dump(db string) (DumpMeta, bool) {
	if db == "" {
		if b.Database == nil {
			return DumpMeta{}, false
		}
		return *b.Database, true
	}
	for _, d := range b.Databases {
		if d.DB == db {
			return d.DumpMeta, true
		}
	}
	return DumpMeta{}, false
}

// backupDBFileOf is the dump file of a database in a backup directory.
func backupDBFileOf(db string) string {
	if db == "" {
		return backupDBFile
	}
	return "database-" + db + ".sql.gz"
}

// backupDBVolumeFileOf is the copy of a database's data volume in a backup directory.
func backupDBVolumeFileOf(db string) string {
	if db == "" {
		return "database.volume.tar.gz"
	}
	return "database-" + db + ".volume.tar.gz"
}

// backupFile is the full content of backup.json (metadata plus the project export).
type backupFile struct {
	BackupMeta
	// Export is the project export in plain text: backups from before secrets were
	// encrypted. New backups carry it sealed with the instance key as SealedExport.
	Export       *projectExport `json:"project,omitempty"`
	SealedExport string         `json:"sealedProject,omitempty"`
}

// newBackupFile builds backup.json's content, the export sealed.
func newBackupFile(meta BackupMeta, p store.Project, jobs []store.CronJob) (backupFile, error) {
	export := exportProject(p, jobs)
	if secrets.Default() == nil {
		return backupFile{BackupMeta: meta, Export: &export}, nil
	}
	raw, err := json.Marshal(export)
	if err != nil {
		return backupFile{}, err
	}
	sealed, err := secrets.Seal(string(raw))
	if err != nil {
		return backupFile{}, err
	}
	return backupFile{BackupMeta: meta, SealedExport: sealed}, nil
}

// projectExport is the desired state of a project, including secrets. It enables a full
// rebuild into a new project (RestoreIntoNewProject); backup.json keeps it sealed (see
// backupFile). Backups from before 0.25 lack the fields marked omitempty, and a project
// restored from one starts without them.
type projectExport struct {
	Name        string                `json:"name"`
	Slug        string                `json:"slug"`
	Path        string                `json:"path"`
	Docroot     string                `json:"docroot"`
	HTTPPort    int                   `json:"httpPort"`
	Services    []exportedService     `json:"services"`
	Env         []exportedEnv         `json:"env"`
	Git         exportedGit           `json:"git"`
	IDEGateway  bool                  `json:"ideGateway,omitempty"`
	Limits      *store.ResourceLimits `json:"limits,omitempty"`
	HealthCheck *store.HealthCheck    `json:"healthCheck,omitempty"`
	ProxyRules  *store.ProxyRules     `json:"proxyRules,omitempty"`
	Workers     []exportedWorker      `json:"workers,omitempty"`
	CronJobs    []exportedCronJob     `json:"cronJobs,omitempty"`
}

type exportedService struct {
	Kind     string             `json:"kind"`
	Variant  string             `json:"variant"`
	Version  string             `json:"version"`
	Config   json.RawMessage    `json:"config"`
	Position int                `json:"position,omitempty"`
	Custom   *store.CustomImage `json:"custom,omitempty"`
}

type exportedWorker struct {
	Name    string   `json:"name"`
	Preset  string   `json:"preset"`
	Args    []string `json:"args,omitempty"`
	Enabled bool     `json:"enabled"`
}

type exportedCronJob struct {
	Name     string        `json:"name"`
	Runtime  string        `json:"runtime"`
	Schedule string        `json:"schedule"`
	Command  string        `json:"command"`
	Timeout  time.Duration `json:"timeout,omitempty"`
	Enabled  bool          `json:"enabled"`
}

type exportedEnv struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

type exportedGit struct {
	URL      string `json:"url"`
	Branch   string `json:"branch"`
	Username string `json:"username"`
	Token    string `json:"token,omitempty"`
}

// BackupInfo is a backup as listed by the API.
type BackupInfo struct {
	ID        string     `json:"id"`
	Dir       string     `json:"dir"`
	Kind      string     `json:"kind"`
	SizeBytes int64      `json:"sizeBytes"`
	CreatedAt time.Time  `json:"createdAt"`
	Meta      BackupMeta `json:"meta"`
	Missing   bool       `json:"missing"`
}

func (m *Manager) backupRoot(slug string) (string, error) {
	p, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return filepath.Join(p.BackupsRoot(), slug), nil
}

func exportProject(p store.Project, jobs []store.CronJob) projectExport {
	ex := projectExport{Name: p.Name, Slug: p.Slug, Path: p.Path, Docroot: p.Docroot, HTTPPort: p.HTTPPort,
		Git:        exportedGit{URL: p.Git.URL, Branch: p.Git.Branch, Username: p.Git.Username, Token: p.Git.Token},
		IDEGateway: p.IDEGateway}
	if p.Limits != (store.ResourceLimits{}) {
		ex.Limits = &p.Limits
	}
	if p.HealthCheck != (store.HealthCheck{}) {
		ex.HealthCheck = &p.HealthCheck
	}
	if raw, _ := json.Marshal(p.ProxyRules); string(raw) != "{}" {
		rules := p.ProxyRules
		ex.ProxyRules = &rules
	}
	for _, s := range p.Services {
		if s.Enabled {
			es := exportedService{Kind: string(s.Kind), Variant: s.Variant, Version: s.Version, Config: s.Config, Position: s.Position}
			if s.Custom.Image != "" || s.Custom.Dockerfile != "" {
				custom := store.CustomImage{Image: s.Custom.Image, Dockerfile: s.Custom.Dockerfile}
				es.Custom = &custom
			}
			ex.Services = append(ex.Services, es)
		}
	}
	for _, e := range p.Env {
		ex.Env = append(ex.Env, exportedEnv{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	for _, w := range p.Workers {
		ex.Workers = append(ex.Workers, exportedWorker{Name: w.Name, Preset: w.Preset, Args: w.Args, Enabled: w.Enabled})
	}
	for _, j := range jobs {
		ex.CronJobs = append(ex.CronJobs, exportedCronJob{Name: j.Name, Runtime: j.Runtime, Schedule: j.Schedule, Command: j.Command, Timeout: j.Timeout, Enabled: j.Enabled})
	}
	return ex
}

// ListBackups returns the backups of a project, newest first.
func (m *Manager) ListBackups(ctx context.Context, id string) ([]BackupInfo, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := m.backupRoot(p.Slug)
	if err != nil {
		return nil, err
	}
	rows, err := m.store.Backups.ListByProject(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]BackupInfo, 0, len(rows))
	for _, b := range rows {
		info := BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt}
		_ = json.Unmarshal(b.Metadata, &info.Meta)
		if _, err := os.Stat(filepath.Join(root, b.Filename, backupMetaFile)); err != nil {
			info.Missing = true
		}
		out = append(out, info)
	}
	return out, nil
}

// backupDirPattern matches the directory names createBackupLocked generates.
var backupDirPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`)

// SweepBackups tidies up after backups that a crash or kill interrupted. A directory
// without backup.json never completed (the metadata is written last) and is removed.
// One that completed but wasn't recorded, because the process died between writing and
// recording, is adopted so it shows up and can be restored. Directories that belong to
// another installation (different project id) or do not look like Envoryx's are left
// alone. It runs at start-up, before the scheduler, when no backup can be in progress.
func (m *Manager) SweepBackups(ctx context.Context) {
	paths, err := m.paths()
	if err != nil {
		return
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return
	}
	for _, p := range projects {
		root := filepath.Join(paths.BackupsRoot(), p.Slug)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		recorded := map[string]bool{}
		if rows, err := m.store.Backups.ListByProject(ctx, p.ID); err == nil {
			for _, b := range rows {
				recorded[b.Filename] = true
			}
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || !backupDirPattern.MatchString(name) || recorded[name] {
				continue
			}
			dir := filepath.Join(root, name)
			raw, err := os.ReadFile(filepath.Join(dir, backupMetaFile))
			if errors.Is(err, os.ErrNotExist) {
				if err := os.RemoveAll(dir); err != nil {
					m.log.Warn("interrupted backup not removed", "project", p.Slug, "dir", name, "err", err)
					continue
				}
				m.log.Warn("removed an interrupted backup", "project", p.Slug, "dir", name)
				continue
			}
			if err != nil {
				continue
			}
			var bf backupFile
			if err := json.Unmarshal(raw, &bf); err != nil || bf.ProjectID != p.ID {
				continue
			}
			kind := "full"
			switch {
			case bf.HasAnyDatabase() && bf.Files == nil:
				kind = "database"
			case !bf.HasAnyDatabase() && bf.Files != nil:
				kind = "files"
			}
			metaJSON, _ := json.Marshal(bf.BackupMeta)
			rec := &store.Backup{ProjectID: p.ID, Filename: name, SizeBytes: dirSize(dir), Kind: kind, Metadata: metaJSON, CreatedAt: bf.CreatedAt}
			if err := m.store.Backups.Create(ctx, rec); err != nil {
				m.log.Warn("unrecorded backup not adopted", "project", p.Slug, "dir", name, "err", err)
				continue
			}
			m.log.Warn("adopted a backup that was not recorded", "project", p.Slug, "dir", name)
		}
	}
}

// CreateBackup dumps the database and/or archives the project files. The project lock is
// held so no lifecycle operation interferes.
func (m *Manager) CreateBackup(ctx context.Context, id string, opts BackupOptions) (BackupInfo, error) {
	var info BackupInfo
	err := m.run(ctx, limitBackup, Operation{Action: "backup", ProjectID: id}, func(ctx context.Context) (err error) {
		info, err = m.createBackup(ctx, id, opts)
		return err
	})
	if err != nil && !errors.Is(err, validate.ErrInvalid) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrBusy) {
		name := id
		if p, perr := m.store.Projects.Get(ctx, id); perr == nil {
			name = p.Name
		}
		m.notify(ctx, notify.Event{Kind: "backup.failed", Level: notify.Error, Project: name, Title: "Backup of " + name + " failed", Message: err.Error()})
	}
	return info, err
}

func (m *Manager) createBackup(ctx context.Context, id string, opts BackupOptions) (BackupInfo, error) {
	if err := validate.UUID(id); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	if !opts.Database && !opts.Files && !opts.Storage {
		return BackupInfo{}, fmt.Errorf("%w: select at least the database, the files or the object storage", validate.ErrInvalid)
	}
	if len(opts.Note) > 500 {
		return BackupInfo{}, fmt.Errorf("%w: note too long", validate.ErrInvalid)
	}
	unlock, err := m.lock(id)
	if err != nil {
		return BackupInfo{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return BackupInfo{}, err
	}
	return m.createBackupLocked(ctx, p, opts)
}

// createBackupLocked does the work of createBackup for callers that already hold the
// project lock (e.g. a database upgrade that must back up first).
func (m *Manager) createBackupLocked(ctx context.Context, p store.Project, opts BackupOptions) (BackupInfo, error) {
	if err := m.checkDiskQuota(); err != nil {
		return BackupInfo{}, err
	}
	paths, err := m.paths()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	root, err := m.backupRoot(p.Slug)
	if err != nil {
		return BackupInfo{}, err
	}
	// A backup that fills the disk is worse than none: files are stored compressed, so
	// their raw size is a safe upper bound; the dump is covered by the reserve.
	var need uint64
	if opts.Files {
		need = uint64(dirSizeSkipping(NewPlanner(paths, m.catalog).ProjectDir(p), !opts.IncludeDependencies))
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup directory: %w", err)
	}
	if err := disk.Require(root, need); err != nil {
		return BackupInfo{}, err
	}
	dirName := time.Now().UTC().Format("20060102-150405") + "-" + store.NewID()[:8]
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup directory: %w", err)
	}
	fail := func(step string, cause error) (BackupInfo, error) {
		_ = os.RemoveAll(dir)
		return BackupInfo{}, fmt.Errorf("%s: %w", step, cause)
	}

	meta := BackupMeta{Format: backupFormat, Envoryx: paths.EnvoryxVersion, ProjectID: p.ID, ProjectName: p.Name, Slug: p.Slug, CreatedAt: time.Now().UTC(), Note: strings.TrimSpace(opts.Note), Source: opts.Source, Runtimes: map[string]string{}}
	for _, s := range p.Services {
		if s.Enabled {
			meta.Runtimes[string(s.Kind)] = s.Variant + " " + s.Version
		}
	}
	// Storage is included when the project has some; requested on its own for a project
	// without, that is an error like a database dump without a database.
	hasStorage := p.Service(store.ServiceStorage) != nil && p.Service(store.ServiceStorage).Enabled
	if opts.Storage && !hasStorage {
		if !opts.Database && !opts.Files {
			return fail("storage", fmt.Errorf("%w: the project has no object storage", validate.ErrInvalid))
		}
		opts.Storage = false
	}
	kind := backupKind(opts)
	// Addon volumes are data like the databases: they go with every database backup that
	// is not limited to one database.
	addonData := opts.Database && opts.OnlyDB == nil && hasAddonVolumes(p)

	if opts.Database {
		if dbs := backupDatabases(p, opts.OnlyDB); len(dbs) == 0 {
			// Without a database there may still be addon volumes to archive below.
			if !addonData {
				if !opts.Files && !opts.Storage {
					return fail("database", fmt.Errorf("%w: the project has no database", validate.ErrInvalid))
				}
				opts.Database = false
				kind = backupKind(opts)
			}
		} else {
			kernel := m.HostKernel(ctx)
			for _, svc := range dbs {
				db := svc.Kind.DatabaseName()
				_, cfg, err := databaseOf(p, db)
				if err != nil {
					return fail("database", err)
				}
				label := cfg.Database
				if db != "" {
					label = db
				}
				if m.kernelProblem(*svc, kernel) != "" {
					// A dump needs the server running, and this one cannot start on the host's
					// kernel (MongoDB 8.0 on Linux 6.19). The data directory itself is what can
					// still be kept, e.g. before an upgrade to a version that opens it.
					step(ctx, "Copying the data volume of the database {{name}}", "name", label)
					file := backupDBVolumeFileOf(db)
					n, err := m.copyDatabaseVolume(ctx, p, svc, filepath.Join(dir, file))
					if err != nil {
						return fail("database volume copy", err)
					}
					meta.DatabaseVolumes = append(meta.DatabaseVolumes, VolumeCopyMeta{DB: db, Type: svc.Variant, Version: svc.Version, File: file, Bytes: n})
					continue
				}
				step(ctx, "Dumping the database {{name}}", "name", label)
				n, err := m.dumpDatabase(ctx, p, svc, cfg, filepath.Join(dir, backupDBFileOf(db)))
				if err != nil {
					return fail("database dump", err)
				}
				dm := DumpMeta{Type: svc.Variant, Version: svc.Version, Name: cfg.Database, Bytes: n}
				if db == "" {
					meta.Database = &dm
				} else {
					meta.Databases = append(meta.Databases, ExtraDumpMeta{DB: db, DumpMeta: dm})
				}
			}
		}
	}
	if addonData {
		files, err := m.backupAddonVolumes(ctx, p, dir)
		if err != nil {
			return fail("addon volumes", err)
		}
		meta.AddonVolumes = files
	}
	if opts.Files {
		step(ctx, "Archiving the project files")
		n, entries, err := archiveDir(NewPlanner(paths, m.catalog).ProjectDir(p), filepath.Join(dir, backupFilesFile), !opts.IncludeDependencies)
		if err != nil {
			return fail("archive files", err)
		}
		meta.Files = &struct {
			Bytes               int64 `json:"bytes"`
			Entries             int   `json:"entries"`
			IncludeDependencies bool  `json:"includeDependencies"`
		}{Bytes: n, Entries: entries, IncludeDependencies: opts.IncludeDependencies}
	}
	if opts.Storage {
		step(ctx, "Exporting the object storage bucket")
		objects, n, err := m.dumpStorage(ctx, p, filepath.Join(dir, backupStorageFile))
		if err != nil {
			return fail("object storage", err)
		}
		_, scfg, _ := storageConfig(p)
		meta.Storage = &struct {
			Bucket  string `json:"bucket"`
			Objects int    `json:"objects"`
			Bytes   int64  `json:"bytes"`
		}{Bucket: scfg.Bucket, Objects: objects, Bytes: n}
	}

	jobs, err := m.store.CronJobs.ListByProject(ctx, p.ID)
	if err != nil {
		return fail("read the cron jobs", err)
	}
	bf, err := newBackupFile(meta, p, jobs)
	if err != nil {
		return fail("write metadata", err)
	}
	content, err := json.MarshalIndent(bf, "", "  ")
	if err != nil {
		return fail("write metadata", err)
	}
	if err := os.WriteFile(filepath.Join(dir, backupMetaFile), content, 0o600); err != nil {
		return fail("write metadata", err)
	}
	size := dirSize(dir)
	metaJSON, _ := json.Marshal(meta)
	rec := &store.Backup{ProjectID: p.ID, Filename: dirName, SizeBytes: size, Kind: kind, Metadata: metaJSON, CreatedAt: meta.CreatedAt}
	if err := m.store.Backups.Create(ctx, rec); err != nil {
		return fail("record backup", err)
	}
	m.audit.Log(ctx, audit.ActionBackupCreated, "project", p.ID, map[string]any{"name": p.Name, "backup": rec.ID, "kind": kind, "bytes": size})
	info := BackupInfo{ID: rec.ID, Dir: dirName, Kind: kind, SizeBytes: size, CreatedAt: rec.CreatedAt, Meta: meta}
	if m.backupHook != nil {
		m.backupHook(p.ID, info)
	}
	return info, nil
}

// backupDatabases are the databases a backup dumps: all of them, or the one named.
func backupDatabases(p store.Project, only *string) []*store.ProjectService {
	var out []*store.ProjectService
	for _, svc := range p.Databases() {
		if only == nil || svc.Kind.DatabaseName() == *only {
			out = append(out, svc)
		}
	}
	return out
}

// backupKind names a backup by its parts: one part → that name, several → "full".
func backupKind(opts BackupOptions) string {
	var parts []string
	if opts.Database {
		parts = append(parts, "database")
	}
	if opts.Files {
		parts = append(parts, "files")
	}
	if opts.Storage {
		parts = append(parts, "storage")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "full"
}

// dumpDatabase streams a logical dump through gzip into target and returns its size.
func (m *Manager) dumpDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, target string) (int64, error) {
	dialect, err := dialectOf(svc)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	gz := gzip.NewWriter(f)
	var stderr strings.Builder
	argv, env := dialect.Dump(cfg)
	code, err := m.dbStream(ctx, dbEnd{p, svc, cfg}, argv, env, nil, gz, &limitedBuilder{b: &stderr})
	if cerr := gz.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, fmt.Errorf("dump failed (exit %d): %s", code, sanitizeSQLError(strings.TrimSpace(stderr.String()), cfg))
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// limitedBuilder keeps only the tail of stderr so error messages stay bounded.
type limitedBuilder struct{ b *strings.Builder }

func (l *limitedBuilder) Write(p []byte) (int, error) {
	if l.b.Len() < 16<<10 {
		l.b.Write(p)
	}
	return len(p), nil
}

// dependencyDirs are dependency and framework build caches (regenerable) skipped from
// file backups unless dependencies are requested.
var dependencyDirs = map[string]bool{"vendor": true, "node_modules": true, ".next": true, ".nuxt": true, ".output": true, ".venv": true, "__pycache__": true}

// archiveDir writes a gzip tarball of root. Entries are relative to root; sockets and
// devices are skipped, symlinks are stored as symlinks.
func archiveDir(root, target string, skipDeps bool) (int64, int, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
	// Files are read through the tree's os.Root: the project's owner can swap an entry
	// for a link to Envoryx's own files while the walk runs.
	tree, err := os.OpenRoot(root)
	if err != nil {
		_ = f.Close()
		return 0, 0, err
	}
	defer tree.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	entries := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && skipDeps && dependencyDirs[d.Name()] {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		entries++
		if info.Mode().IsRegular() {
			src, _, err := openProjectFile(tree, rel)
			if err != nil {
				return err
			}
			_, err = io.CopyN(tw, src, hdr.Size)
			_ = src.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err := tw.Close(); err != nil && walkErr == nil {
		walkErr = err
	}
	if err := gz.Close(); err != nil && walkErr == nil {
		walkErr = err
	}
	if err := f.Close(); err != nil && walkErr == nil {
		walkErr = err
	}
	if walkErr != nil {
		return 0, 0, walkErr
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, 0, err
	}
	return info.Size(), entries, nil
}

func dirSize(dir string) int64 { return dirSizeSkipping(dir, false) }

// dirSizeSkipping sums file sizes, optionally leaving out vendor/ and node_modules/ like
// the file archive does; it is the space estimate before a backup starts.
func dirSizeSkipping(dir string, skipDeps bool) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDeps && dependencyDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

// DeleteBackup removes a backup directory and its record.
func (m *Manager) DeleteBackup(ctx context.Context, id, backupID string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := validate.UUID(backupID); err != nil {
		return ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return err
	}
	b, err := m.store.Backups.Get(ctx, id, backupID)
	if err != nil {
		return err
	}
	dir, err := m.backupDir(p.Slug, b.Filename)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove backup files: %w", err)
	}
	if err := m.store.Backups.Delete(ctx, id, backupID); err != nil {
		return err
	}
	// Copies on offsite targets stay there; only the local bookkeeping goes.
	_ = m.store.Offsite.DeleteByBackup(ctx, store.OffsiteProject, backupID)
	m.audit.Log(ctx, audit.ActionBackupDeleted, "project", id, map[string]any{"name": p.Name, "backup": backupID})
	return nil
}

// backupDir resolves a backup directory and guards against escaping the backup root.
func (m *Manager) backupDir(slug, name string) (string, error) {
	root, err := m.backupRoot(slug)
	if err != nil {
		return "", err
	}
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return "", fmt.Errorf("%w: invalid backup name", validate.ErrInvalid)
	}
	return filepath.Join(root, name), nil
}

// OpenBackupArchive streams the whole backup directory as an uncompressed tar (its members
// are compressed already). The caller must close the reader.
func (m *Manager) OpenBackupArchive(ctx context.Context, id, backupID string) (io.ReadCloser, string, error) {
	if err := validate.UUID(id); err != nil {
		return nil, "", ErrNotFound
	}
	if err := validate.UUID(backupID); err != nil {
		return nil, "", ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	b, err := m.store.Backups.Get(ctx, id, backupID)
	if err != nil {
		return nil, "", err
	}
	dir, err := m.backupDir(p.Slug, b.Filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(filepath.Join(dir, backupMetaFile)); err != nil {
		return nil, "", fmt.Errorf("%w: backup files are missing", store.ErrNotFound)
	}
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		var werr error
		for _, name := range backupMembersIn(dir) {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				werr = err
				break
			}
			hdr.Name = p.Slug + "-" + b.Filename + "/" + name
			if err := tw.WriteHeader(hdr); err != nil {
				werr = err
				break
			}
			f, err := os.Open(path)
			if err != nil {
				werr = err
				break
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			if err != nil {
				werr = err
				break
			}
		}
		if err := tw.Close(); err != nil && werr == nil {
			werr = err
		}
		pw.CloseWithError(werr)
	}()
	return pr, p.Slug + "-" + b.Filename + ".tar", nil
}

// RestoreBackup restores the database and/or files of a backup into the project.
// Destructive: the database is overwritten by the dump; files are extracted over the
// project directory (optionally after wiping it).
func (m *Manager) RestoreBackup(ctx context.Context, id, backupID string, opts RestoreOptions) (BackupInfo, error) {
	if err := validate.UUID(id); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	if err := validate.UUID(backupID); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	if !opts.Database && !opts.Files && !opts.Storage {
		return BackupInfo{}, fmt.Errorf("%w: select the database, the files and/or the object storage to restore", validate.ErrInvalid)
	}
	var info BackupInfo
	err := m.run(ctx, limitBackup, Operation{Action: "restore", ProjectID: id}, func(ctx context.Context) (err error) {
		info, err = m.restoreBackup(ctx, id, backupID, opts)
		return err
	})
	return info, err
}

func (m *Manager) restoreBackup(ctx context.Context, id, backupID string, opts RestoreOptions) (BackupInfo, error) {
	unlock, err := m.lock(id)
	if err != nil {
		return BackupInfo{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return BackupInfo{}, err
	}
	if opts.Confirm != p.Slug {
		return BackupInfo{}, fmt.Errorf("%w: confirmation must equal the project identifier %q", validate.ErrInvalid, p.Slug)
	}
	b, err := m.store.Backups.Get(ctx, id, backupID)
	if err != nil {
		return BackupInfo{}, err
	}
	dir, err := m.backupDir(p.Slug, b.Filename)
	if err != nil {
		return BackupInfo{}, err
	}
	var meta BackupMeta
	_ = json.Unmarshal(b.Metadata, &meta)
	paths, err := m.paths()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	restored := map[string]any{"name": p.Name, "backup": backupID}

	if opts.Database {
		if (meta.HasAnyDatabase() && len(p.Databases()) > 0) || len(meta.AddonVolumes) == 0 {
			restoredDBs, skipped, err := m.restoreDatabases(ctx, p, meta, dir, nil)
			if err != nil {
				return BackupInfo{}, err
			}
			restored["database"] = true
			if len(restoredDBs) > 0 {
				restored["databases"] = restoredDBs
			}
			if len(skipped) > 0 {
				restored["skipped"] = skipped
			}
		}
		if len(meta.AddonVolumes) > 0 {
			vols, err := m.restoreAddonVolumes(ctx, p, dir, meta.AddonVolumes)
			if err != nil {
				return BackupInfo{}, fmt.Errorf("restore addon volumes: %w", err)
			}
			restored["addonVolumes"] = vols
		}
	}
	if opts.Files {
		if meta.Files == nil {
			return BackupInfo{}, fmt.Errorf("%w: this backup contains no files", validate.ErrInvalid)
		}
		target := NewPlanner(paths, m.catalog).ProjectDir(p)
		if opts.WipeFiles {
			if err := wipeDir(target); err != nil {
				return BackupInfo{}, restoreIncomplete(restored, "emptying the project directory", target, err)
			}
		}
		step(ctx, "Restoring the project files")
		if err := extractArchive(filepath.Join(dir, backupFilesFile), target, paths.PUID, paths.PGID); err != nil {
			if errors.Is(err, validate.ErrInvalid) {
				return BackupInfo{}, fmt.Errorf("restore files: %w", err)
			}
			return BackupInfo{}, restoreIncomplete(restored, "restoring the files", target, err)
		}
		restored["files"] = true
		restored["wiped"] = opts.WipeFiles
	}
	if opts.Storage {
		if meta.Storage == nil {
			return BackupInfo{}, fmt.Errorf("%w: this backup contains no object storage", validate.ErrInvalid)
		}
		step(ctx, "Restoring the object storage bucket")
		if err := m.restoreStorage(ctx, p, filepath.Join(dir, backupStorageFile), opts.WipeStorage); err != nil {
			return BackupInfo{}, fmt.Errorf("restore object storage: %w", err)
		}
		restored["storage"] = true
		restored["storageWiped"] = opts.WipeStorage
	}
	if opts.FlushRedis && (restored["database"] == true || opts.Files) {
		flushed, err := m.flushRedis(ctx, p)
		if err != nil {
			m.log.Warn("emptying Redis after the restore failed", "project", p.Slug, "err", err)
			restored["redisFlushFailed"] = err.Error()
		} else if flushed {
			restored["redisFlushed"] = true
		}
	}
	m.audit.Log(ctx, audit.ActionBackupRestored, "project", id, restored)
	return BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}, nil
}

// restoreIncomplete explains a restore that stopped on the way: which step failed and
// on which file (relative to the project), and what was restored before, since that is
// not rolled back.
func restoreIncomplete(restored map[string]any, what, dir string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		if rel, rerr := filepath.Rel(dir, pe.Path); rerr == nil && !strings.HasPrefix(rel, "..") {
			err = fmt.Errorf("%s %s: %w", pe.Op, rel, pe.Err)
		}
	}
	msg := fmt.Sprintf("%s failed: %v", what, err)
	if restored["database"] == true {
		msg += ". The database was already restored"
	}
	msg += ". The files are only partly restored; fix the cause (file permissions, say) and restore the files again"
	return fmt.Errorf("%w: %s", ErrRestoreIncomplete, msg)
}

// restoreDatabases puts the dumps of a backup back: every database the backup and the
// project both have, or only the one named by only. Dumps of databases the project no
// longer has are skipped and named; nothing to restore at all is an error.
func (m *Manager) restoreDatabases(ctx context.Context, p store.Project, meta BackupMeta, dir string, only *string) (restored, skipped []string, err error) {
	type target struct {
		db   string
		dump DumpMeta
		// volume is set for a copy of the data volume instead of a dump.
		volume *VolumeCopyMeta
	}
	var targets []target
	if meta.Database != nil {
		targets = append(targets, target{"", *meta.Database, nil})
	}
	for _, d := range meta.Databases {
		targets = append(targets, target{d.DB, d.DumpMeta, nil})
	}
	for i, v := range meta.DatabaseVolumes {
		targets = append(targets, target{v.DB, DumpMeta{Type: v.Type, Version: v.Version}, &meta.DatabaseVolumes[i]})
	}
	count := 0
	for _, t := range targets {
		if only != nil && t.db != *only {
			continue
		}
		svc, cfg, err := databaseOf(p, t.db)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				skipped = append(skipped, dbLabel(t.db))
				continue
			}
			return nil, nil, err
		}
		if err := checkDumpType(t.dump.Type, svc); err != nil {
			return nil, nil, err
		}
		if t.db == "" {
			step(ctx, "Restoring the database")
		} else {
			step(ctx, "Restoring the database {{name}}", "name", t.db)
		}
		if t.volume != nil {
			if err := m.restoreDatabaseVolume(ctx, p, svc, *t.volume, filepath.Join(dir, backupDBVolumeFileOf(t.db))); err != nil {
				return nil, nil, fmt.Errorf("restore database %s: %w", dbLabel(t.db), err)
			}
		} else if err := m.restoreDatabase(ctx, p, svc, cfg, filepath.Join(dir, backupDBFileOf(t.db))); err != nil {
			return nil, nil, fmt.Errorf("restore database %s: %w", dbLabel(t.db), err)
		}
		if t.db != "" {
			restored = append(restored, t.db)
		}
		count++
	}
	if count == 0 {
		if len(targets) == 0 || only != nil && !meta.HasDatabase(*only) {
			return nil, nil, fmt.Errorf("%w: this backup contains no database dump", validate.ErrInvalid)
		}
		return nil, nil, fmt.Errorf("%w: the project has none of the databases this backup holds (%s)", validate.ErrInvalid, strings.Join(skipped, ", "))
	}
	return restored, skipped, nil
}

// dbLabel names a database in messages: its name, or "the primary database".
func dbLabel(db string) string {
	if db == "" {
		return "the primary database"
	}
	return db
}

// checkDumpType reports whether a dump of the flavour dumpType fits the database: one
// flavour's dump is not another's.
func checkDumpType(dumpType string, svc *store.ProjectService) error {
	if dumpType != svc.Variant {
		return fmt.Errorf("%w: the dump is for %s but the database is %s", validate.ErrInvalid, dumpType, svc.Variant)
	}
	return nil
}

func (m *Manager) restoreDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, dump string) error {
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	f, err := os.Open(dump)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read dump: %w", err)
	}
	defer gz.Close()
	// A stopped project's database is started for the import and stopped again, and a
	// just created one (a restore into a new project) is waited for.
	return m.withServiceRunning(ctx, p, svc.Kind, func(ctx context.Context) error {
		if err := m.waitForDatabase(ctx, p, svc, cfg, dialect); err != nil {
			return err
		}
		var stderr strings.Builder
		argv, env := dialect.Restore(cfg)
		code, err := m.dbStream(ctx, dbEnd{p, svc, cfg}, argv, env, gz, nil, &limitedBuilder{b: &stderr})
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("import failed (exit %d): %s", code, sanitizeSQLError(strings.TrimSpace(stderr.String()), cfg))
		}
		return nil
	})
}

// wipeDir removes the contents of dir but keeps the directory itself.
func wipeDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := removeAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeAll is os.RemoveAll that also gets through read-only directories (Drupal makes
// sites/default 0555): Envoryx not running as root may not delete from them until it
// gives itself write access.
func removeAll(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			if info, err := d.Info(); err == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
	return os.RemoveAll(path)
}

// extractArchive unpacks a gzip tarball into target with tar-slip protection: entry names
// must stay inside target, and symlinks may not point outside of it.
func extractArchive(archive, target string, uid, gid int) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	// Everything goes through an os.Root of the target: the project's owner may have put
	// a symbolic link into the directory a restore without wiping writes into (d ->
	// /config), and Envoryx, running as root, must not create, chmod or chown anything
	// through it outside the project.
	root, err := os.OpenRoot(realTarget)
	if err != nil {
		return err
	}
	defer root.Close()
	chown := func(rel string) {
		if os.Geteuid() == 0 {
			_ = root.Lchown(rel, uid, gid)
		}
	}
	inside := func(path string) bool {
		rel, err := filepath.Rel(realTarget, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	// Read-only directories (Drupal's sites/default is 0555) get write access while
	// files go into them; afterwards every directory gets its mode back: the one from
	// the archive, or the one it had before for a directory the archive does not hold.
	dirModes := map[string]fs.FileMode{}
	var dirOrder []string
	keepMode := func(dir string, mode fs.FileMode) {
		if _, ok := dirModes[dir]; !ok {
			dirOrder = append(dirOrder, dir)
		}
		dirModes[dir] = mode
	}
	writable := func(dir string) {
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o700 == 0o700 {
			return
		}
		if _, ok := dirModes[dir]; !ok {
			keepMode(dir, info.Mode().Perm())
		}
		_ = root.Chmod(dir, info.Mode().Perm()|0o700)
	}
	defer func() {
		// Deepest first, so a parent made read-only again does not block its children.
		for i := len(dirOrder) - 1; i >= 0; i-- {
			_ = root.Chmod(dirOrder[i], dirModes[dirOrder[i]])
		}
	}()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%w: archive entry %q escapes the project directory", validate.ErrInvalid, hdr.Name)
		}
		parent := filepath.Dir(name)
		if err := root.MkdirAll(parent, 0o755); err != nil {
			if strings.Contains(err.Error(), "path escapes") {
				return fmt.Errorf("%w: archive entry %q escapes the project directory", validate.ErrInvalid, hdr.Name)
			}
			return err
		}
		writable(parent)
		switch hdr.Typeflag {
		case tar.TypeDir:
			mode := os.FileMode(hdr.Mode) & 0o777
			if err := root.MkdirAll(name, mode|0o700); err != nil {
				return err
			}
			if err := root.Chmod(name, mode|0o700); err != nil {
				return err
			}
			keepMode(name, mode)
			chown(name)
		case tar.TypeSymlink:
			linkDest := hdr.Linkname
			if !filepath.IsAbs(linkDest) {
				linkDest = filepath.Join(realTarget, parent, linkDest)
			}
			if !inside(filepath.Clean(linkDest)) {
				continue // skip links pointing outside the project
			}
			_ = root.RemoveAll(name)
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
			chown(name)
		case tar.TypeReg:
			if info, err := root.Lstat(name); err == nil {
				switch {
				case info.Mode()&os.ModeSymlink != 0:
					_ = root.Remove(name) // never write through an existing symlink
				case info.Mode().IsRegular() && info.Mode().Perm()&0o200 == 0:
					// A read-only file (Drupal's settings.php is 0444) is replaced all
					// the same; it gets the archive's mode back below.
					_ = root.Chmod(name, info.Mode().Perm()|0o200)
				}
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Chmod(mode); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			chown(name)
		default:
			// hard links, devices, fifos: not restored
		}
	}
	return nil
}

// ResealBackups seals the project export of every backup.json with the current key:
// plain exports from before encryption and exports sealed with an older key. It returns
// how many files it changed.
func (m *Manager) ResealBackups(ctx context.Context) (int, error) {
	r := secrets.Default()
	if r == nil {
		return 0, nil
	}
	root, err := m.sealRoots()
	if err != nil {
		m.log.Warn("project backups not resealed: the paths are not configured yet", "err", err)
		return 0, nil
	}
	files, err := filepath.Glob(filepath.Join(root.BackupsRoot(), "*", "*", backupMetaFile))
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, f := range files {
		if ctx.Err() != nil {
			return changed, ctx.Err()
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return changed, err
		}
		var bf backupFile
		if err := json.Unmarshal(raw, &bf); err != nil {
			m.log.Warn("backup.json not resealed: unreadable", "file", f, "err", err)
			continue
		}
		switch {
		case bf.Export != nil:
			plain, err := json.Marshal(bf.Export)
			if err != nil {
				return changed, err
			}
			if bf.SealedExport, err = r.Seal(string(plain)); err != nil {
				return changed, err
			}
			bf.Export = nil
		case r.NeedsReseal(bf.SealedExport):
			v, _, err := r.Reseal(bf.SealedExport)
			if err != nil {
				return changed, fmt.Errorf("%s: %w", f, err)
			}
			bf.SealedExport = v
		default:
			continue
		}
		out, err := json.MarshalIndent(bf, "", "  ")
		if err != nil {
			return changed, err
		}
		tmp := f + ".tmp"
		if err := os.WriteFile(tmp, out, 0o600); err != nil {
			return changed, err
		}
		if err := os.Rename(tmp, f); err != nil {
			_ = os.Remove(tmp)
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// flushRedis empties the Redis that Envoryx runs for the project. A stopped Redis keeps
// its data on the volume, so it is started for the flush and stopped again. It reports
// whether there was one to empty.
func (m *Manager) flushRedis(ctx context.Context, p store.Project) (bool, error) {
	svc := p.Service(store.ServiceRedis)
	if svc == nil || !svc.Enabled {
		return false, nil
	}
	var cfg runtime.ServiceConfig
	if len(svc.Config) > 0 {
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return false, err
		}
	}
	if cfg.External() {
		return false, nil
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(containers, func(c docker.Container) bool { return c.Service() == string(store.ServiceRedis) })
	if i < 0 {
		return false, nil // never started: nothing cached
	}
	c := containers[i]
	step(ctx, "Emptying Redis")
	if c.State != "running" {
		if err := m.engine.StartContainer(ctx, c.ID); err != nil {
			return false, err
		}
		defer func() {
			if err := m.engine.StopContainer(context.WithoutCancel(ctx), c.ID, 10*time.Second); err != nil {
				m.log.Warn("stopping Redis again failed", "project", p.Slug, "err", err)
			}
		}()
	}
	// A Redis that was just started loads its append-only file first and answers LOADING.
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := m.engine.Exec(ctx, c.ID, []string{"redis-cli", "FLUSHALL"}, nil)
		if err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) == "OK" {
			return true, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("redis-cli FLUSHALL: %s", strings.TrimSpace(res.Stdout+" "+res.Stderr))
			}
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

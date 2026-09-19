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
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Backup layout under /config/backups/<slug>/<dir>/:
//
//	backup.json      metadata + project export (services, env, credentials)
//	database.sql.gz  logical dump of the primary database (optional)
//	files.tar.gz     project directory (optional)
const (
	backupMetaFile  = "backup.json"
	backupDBFile    = "database.sql.gz"
	backupFilesFile = "files.tar.gz"
	backupFormat    = 1
)

// BackupOptions select what a backup contains.
type BackupOptions struct {
	Database bool
	Files    bool
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
	// WipeFiles empties the project directory before extracting.
	WipeFiles bool
	Confirm   string
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
	Database    *struct {
		Type    string `json:"type"`
		Version string `json:"version"`
		Name    string `json:"name"`
		Bytes   int64  `json:"bytes"`
	} `json:"database,omitempty"`
	Files *struct {
		Bytes               int64 `json:"bytes"`
		Entries             int   `json:"entries"`
		IncludeDependencies bool  `json:"includeDependencies"`
	} `json:"files,omitempty"`
	Runtimes map[string]string `json:"runtimes"`
}

// backupFile is the full content of backup.json (metadata plus the project export).
type backupFile struct {
	BackupMeta
	Export projectExport `json:"project"`
}

// projectExport is the desired state of a project, including secrets. It enables a full
// rebuild (also into a new project later). Backups live under /config, which is protected.
type projectExport struct {
	Name     string            `json:"name"`
	Slug     string            `json:"slug"`
	Path     string            `json:"path"`
	Docroot  string            `json:"docroot"`
	HTTPPort int               `json:"httpPort"`
	Services []exportedService `json:"services"`
	Env      []exportedEnv     `json:"env"`
	Git      exportedGit       `json:"git"`
}

type exportedService struct {
	Kind    string          `json:"kind"`
	Variant string          `json:"variant"`
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config"`
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

func exportProject(p store.Project) projectExport {
	ex := projectExport{Name: p.Name, Slug: p.Slug, Path: p.Path, Docroot: p.Docroot, HTTPPort: p.HTTPPort,
		Git: exportedGit{URL: p.Git.URL, Branch: p.Git.Branch, Username: p.Git.Username, Token: p.Git.Token}}
	for _, s := range p.Services {
		if s.Enabled {
			ex.Services = append(ex.Services, exportedService{Kind: string(s.Kind), Variant: s.Variant, Version: s.Version, Config: s.Config})
		}
	}
	for _, e := range p.Env {
		ex.Env = append(ex.Env, exportedEnv{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
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

// CreateBackup dumps the database and/or archives the project files. The project lock is
// held so no lifecycle operation interferes.
func (m *Manager) CreateBackup(ctx context.Context, id string, opts BackupOptions) (BackupInfo, error) {
	info, err := m.createBackup(ctx, id, opts)
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
	if !opts.Database && !opts.Files {
		return BackupInfo{}, fmt.Errorf("%w: select at least the database or the files", validate.ErrInvalid)
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
	kind := "full"
	if opts.Database != opts.Files {
		if opts.Database {
			kind = "database"
		} else {
			kind = "files"
		}
	}

	if opts.Database {
		svc, cfg, err := databaseConfig(p)
		if err != nil {
			if errors.Is(err, ErrNoDatabase) {
				if !opts.Files {
					return fail("database", fmt.Errorf("%w: the project has no database", validate.ErrInvalid))
				}
				kind = "files"
			} else {
				return fail("database", err)
			}
		} else {
			n, err := m.dumpDatabase(ctx, p, svc, cfg, filepath.Join(dir, backupDBFile))
			if err != nil {
				return fail("database dump", err)
			}
			meta.Database = &struct {
				Type    string `json:"type"`
				Version string `json:"version"`
				Name    string `json:"name"`
				Bytes   int64  `json:"bytes"`
			}{Type: svc.Variant, Version: svc.Version, Name: cfg.Database, Bytes: n}
		}
	}
	if opts.Files {
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

	content, err := json.MarshalIndent(backupFile{BackupMeta: meta, Export: exportProject(p)}, "", "  ")
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
	return BackupInfo{ID: rec.ID, Dir: dirName, Kind: kind, SizeBytes: size, CreatedAt: rec.CreatedAt, Meta: meta}, nil
}

// dumpDatabase streams a logical dump through gzip into target and returns its size.
func (m *Manager) dumpDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, target string) (int64, error) {
	dialect, err := dialectOf(svc)
	if err != nil {
		return 0, err
	}
	c, err := m.ServiceContainer(ctx, p.ID, store.ServiceDatabase)
	if err != nil {
		return 0, err
	}
	if c.State != "running" {
		return 0, fmt.Errorf("%w: the database container must be running", ErrConflict)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	gz := gzip.NewWriter(f)
	var stderr strings.Builder
	argv, env := dialect.Dump(cfg)
	code, err := m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{Cmd: argv, Env: env, Stdout: gz, Stderr: &limitedBuilder{b: &stderr}})
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

// dependencyDirs are skipped from file backups unless dependencies are requested.
var dependencyDirs = map[string]bool{"vendor": true, "node_modules": true}

// archiveDir writes a gzip tarball of root. Entries are relative to root; sockets and
// devices are skipped, symlinks are stored as symlinks.
func archiveDir(root, target string, skipDeps bool) (int64, int, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
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
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
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
		for _, name := range []string{backupMetaFile, backupDBFile, backupFilesFile} {
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
	if !opts.Database && !opts.Files {
		return BackupInfo{}, fmt.Errorf("%w: select the database and/or the files to restore", validate.ErrInvalid)
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
		svc, cfg, err := databaseConfig(p)
		if err != nil {
			return BackupInfo{}, err
		}
		if meta.Database == nil {
			return BackupInfo{}, fmt.Errorf("%w: this backup contains no database dump", validate.ErrInvalid)
		}
		if meta.Database.Type != svc.Variant {
			return BackupInfo{}, fmt.Errorf("%w: the dump is for %s but the project uses %s", validate.ErrInvalid, meta.Database.Type, svc.Variant)
		}
		if err := m.restoreDatabase(ctx, p, svc, cfg, filepath.Join(dir, backupDBFile)); err != nil {
			return BackupInfo{}, fmt.Errorf("restore database: %w", err)
		}
		restored["database"] = true
	}
	if opts.Files {
		if meta.Files == nil {
			return BackupInfo{}, fmt.Errorf("%w: this backup contains no files", validate.ErrInvalid)
		}
		target := NewPlanner(paths, m.catalog).ProjectDir(p)
		if opts.WipeFiles {
			if err := wipeDir(target); err != nil {
				return BackupInfo{}, fmt.Errorf("wipe project directory: %w", err)
			}
		}
		if err := extractArchive(filepath.Join(dir, backupFilesFile), target, paths.PUID, paths.PGID); err != nil {
			return BackupInfo{}, fmt.Errorf("restore files: %w", err)
		}
		restored["files"] = true
		restored["wiped"] = opts.WipeFiles
	}
	m.audit.Log(ctx, audit.ActionBackupRestored, "project", id, restored)
	return BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}, nil
}

func (m *Manager) restoreDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, dump string) error {
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	c, err := m.ServiceContainer(ctx, p.ID, store.ServiceDatabase)
	if err != nil {
		return err
	}
	if c.State != "running" {
		return fmt.Errorf("%w: the database container must be running", ErrConflict)
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
	var stderr strings.Builder
	argv, env := dialect.Restore(cfg)
	code, err := m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{Cmd: argv, Env: env, Stdin: gz, Stderr: &limitedBuilder{b: &stderr}})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("import failed (exit %d): %s", code, sanitizeSQLError(strings.TrimSpace(stderr.String()), cfg))
	}
	return nil
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
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
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
	chown := func(path string) {
		if os.Geteuid() == 0 {
			_ = os.Lchown(path, uid, gid)
		}
	}
	inside := func(path string) bool {
		rel, err := filepath.Rel(realTarget, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: archive entry %q escapes the project directory", validate.ErrInvalid, hdr.Name)
		}
		dest := filepath.Join(realTarget, name)
		// The parent must resolve inside the target even if an earlier symlink was extracted.
		parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			parent = filepath.Dir(dest)
		}
		if !inside(parent) {
			return fmt.Errorf("%w: archive entry %q escapes the project directory", validate.ErrInvalid, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return err
			}
			chown(dest)
		case tar.TypeSymlink:
			linkDest := hdr.Linkname
			if !filepath.IsAbs(linkDest) {
				linkDest = filepath.Join(filepath.Dir(dest), linkDest)
			}
			if !inside(filepath.Clean(linkDest)) {
				continue // skip links pointing outside the project
			}
			_ = os.RemoveAll(dest)
			if err := os.Symlink(hdr.Linkname, dest); err != nil {
				return err
			}
			chown(dest)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(dest) // never write through an existing symlink
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			chown(dest)
		default:
			// hard links, devices, fifos: not restored
		}
	}
	return nil
}

// Package instance creates and restores backups of the Envoryx instance itself: the
// SQLite database (users, sessions, tokens, projects, settings), the local CA, the SSH
// host and deploy keys, notification settings and the generated per-project
// configuration. Project files and Docker volumes are not part of it – those are covered
// by project backups.
//
// A backup is a single gzip tarball:
//
//	instance.json      metadata (always the first entry, so listing stays cheap)
//	envoryx.db         consistent copy of the database (VACUUM INTO)
//	config/...         the config directory without caches, databases and backups
//
// Restoring replaces the config directory, which cannot happen while the server is
// using it. ScheduleRestore records the request; the next start applies it before the
// database is opened (ApplyPendingRestore) after taking a safety backup of the state
// it is about to replace.
package instance

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/validate"
)

const (
	metaFile      = "instance.json"
	dbFile        = "envoryx.db"
	configPrefix  = "config/"
	format        = 1
	pendingMarker = ".restore-pending"

	// KindManual is a backup requested by the user, KindUpload one they imported,
	// KindPreMigrate/KindPreRestore are taken automatically before the schema changes or
	// a restore replaces the configuration, KindScheduled daily for offsite targets.
	KindManual     = "manual"
	KindUpload     = "upload"
	KindPreMigrate = "pre-migrate"
	KindPreRestore = "pre-restore"
	KindScheduled  = "scheduled"

	// keepAutomatic is how many backups of each automatic kind are kept.
	keepAutomatic = 5
	// maxNote bounds the free-text note.
	maxNote = 500
)

// ErrNotFound is returned for unknown backup IDs.
var ErrNotFound = errors.New("instance backup not found")

// ErrPending is returned when a restore is already scheduled.
var ErrPending = errors.New("a restore is already scheduled; restart Envoryx to apply it")

var idPattern = regexp.MustCompile(`^(manual|upload|pre-migrate|pre-restore|scheduled)-[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// Meta is stored as instance.json and returned by the API.
type Meta struct {
	Format    int       `json:"format"`
	Envoryx   string    `json:"envoryx"`
	Schema    int       `json:"schema"`
	CreatedAt time.Time `json:"createdAt"`
	Kind      string    `json:"kind"`
	Note      string    `json:"note,omitempty"`
	// Entries counts the config files in the archive (not the database).
	Entries int `json:"entries"`
}

// Info is a backup as listed by the API.
type Info struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
	Meta      Meta      `json:"meta"`
}

// Pending describes a scheduled restore.
type Pending struct {
	ID          string    `json:"id"`
	RequestedAt time.Time `json:"requestedAt"`
	// RequestedBy is the audit actor who scheduled the restore, kept in the marker so the
	// restored database can record who did it.
	RequestedBy string `json:"requestedBy,omitempty"`
}

// Applied reports a restore that ApplyPendingRestore carried out.
type Applied struct {
	Pending
	// PreRestoreID is the safety backup of the state that was replaced ("" when there
	// was no database yet).
	PreRestoreID string
}

// Store manages the instance backups in Dir.
type Store struct {
	// ConfigDir is the directory that is backed up (/config).
	ConfigDir string
	// DBPath is the SQLite database (usually inside ConfigDir).
	DBPath string
	// Dir is where the backups live; it must not be inside the parts of ConfigDir that
	// are archived (the default <backups>/_instance is fine either way, see skip).
	Dir string
	// Version is the running Envoryx version, recorded in the metadata.
	Version string
	// LatestSchema is the schema version this binary supports; newer backups are refused.
	LatestSchema int
	Log          *slog.Logger
}

// Create writes a backup of kind with an optional note using sqlDB for the database copy.
func (s *Store) Create(ctx context.Context, sqlDB *sql.DB, kind, note string) (Info, error) {
	note = strings.TrimSpace(note)
	if len(note) > maxNote {
		return Info{}, fmt.Errorf("%w: note too long", validate.ErrInvalid)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Info{}, fmt.Errorf("create instance backup directory: %w", err)
	}
	// The database copy is written uncompressed next to the archive first, so twice its
	// size must be free; the config files are small in comparison.
	var dbSize uint64
	if st, err := os.Stat(s.DBPath); err == nil {
		dbSize = uint64(st.Size())
	}
	if err := disk.Require(s.Dir, 2*dbSize); err != nil {
		return Info{}, err
	}
	id := newID(kind)
	target := filepath.Join(s.Dir, id+".tar.gz")
	tmpDB := filepath.Join(s.Dir, "."+id+".db")
	defer os.Remove(tmpDB)

	schema, err := schemaVersion(ctx, sqlDB)
	if err != nil {
		return Info{}, fmt.Errorf("read schema version: %w", err)
	}
	// VACUUM INTO writes a consistent, compacted copy while the database stays in use.
	if _, err := sqlDB.ExecContext(ctx, `VACUUM INTO ?`, tmpDB); err != nil {
		return Info{}, fmt.Errorf("copy database: %w", err)
	}

	meta := Meta{Format: format, Envoryx: s.Version, Schema: schema, CreatedAt: time.Now().UTC(), Kind: kind, Note: note}
	// The archive grows under a name List ignores and is renamed once complete, so a
	// crash mid-way never leaves a plausible-looking but truncated backup behind.
	partial := target + partialSuffix
	entries, err := s.writeArchive(partial, tmpDB, &meta)
	if err != nil {
		_ = os.Remove(partial)
		return Info{}, err
	}
	if err := os.Rename(partial, target); err != nil {
		_ = os.Remove(partial)
		return Info{}, fmt.Errorf("finish archive: %w", err)
	}
	meta.Entries = entries
	st, err := os.Stat(target)
	if err != nil {
		return Info{}, err
	}
	s.prune(kind)
	return Info{ID: id, Kind: kind, SizeBytes: st.Size(), CreatedAt: meta.CreatedAt, Meta: meta}, nil
}

// writeArchive builds the tarball. The metadata is written first with the entry count
// filled in, which requires counting the config files before archiving them.
func (s *Store) writeArchive(target, dbCopy string, meta *Meta) (int, error) {
	files, err := s.configFiles()
	if err != nil {
		return 0, fmt.Errorf("scan config directory: %w", err)
	}
	meta.Entries = len(files)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func() error {
		raw, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: metaFile, Mode: 0o600, Size: int64(len(raw)), ModTime: meta.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(raw); err != nil {
			return err
		}
		if err := addFile(tw, dbFile, dbCopy, 0o600); err != nil {
			return fmt.Errorf("add database: %w", err)
		}
		for _, rel := range files {
			if err := addFile(tw, configPrefix+filepath.ToSlash(rel), filepath.Join(s.ConfigDir, rel), 0); err != nil {
				return fmt.Errorf("add %s: %w", rel, err)
			}
		}
		return nil
	}
	werr := write()
	if err := tw.Close(); err != nil && werr == nil {
		werr = err
	}
	if err := gz.Close(); err != nil && werr == nil {
		werr = err
	}
	if err := f.Close(); err != nil && werr == nil {
		werr = err
	}
	return len(files), werr
}

// addFile appends a regular file; mode 0 keeps the file's own permission bits.
func addFile(tw *tar.Writer, name, path string, mode int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Uname, hdr.Gname = "", ""
	if mode != 0 {
		hdr.Mode = mode
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(tw, src)
	return err
}

// configFiles lists the regular files under ConfigDir that belong into a backup, as
// paths relative to ConfigDir. Symlinks and special files are skipped.
func (s *Store) configFiles() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.ConfigDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == s.ConfigDir {
				return err
			}
			return nil // unreadable entries are left out rather than failing the backup
		}
		rel, err := filepath.Rel(s.ConfigDir, path)
		if err != nil || rel == "." {
			return err
		}
		if s.skip(path, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

// skip decides what stays out of the archive: the database files (copied separately),
// caches, project backups, the log history, the instance backups themselves and the
// restore marker.
func (s *Store) skip(path, rel string, isDir bool) bool {
	if within(path, s.Dir) {
		return true
	}
	if !isDir && within(path, filepath.Dir(s.DBPath)) && strings.HasPrefix(filepath.Base(path), filepath.Base(s.DBPath)) {
		return true // envoryx.db, -wal, -shm: the database is copied with VACUUM INTO
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch parts[0] {
	case "backups", "jetbrains", "logs", pendingMarker:
		return true
	case "projects":
		// projects/<id>/home holds composer/npm caches of the project user.
		return isDir && len(parts) == 3 && parts[2] == "home"
	}
	return false
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// partialSuffix marks an archive that is still being written.
const partialSuffix = ".partial"

// Sweep removes what an interrupted backup left behind (a partial archive, a database
// copy) and reports how many. It runs at start-up, when no backup can be in progress.
func (s *Store) Sweep() int {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		leftover := strings.HasSuffix(name, ".tar.gz"+partialSuffix) ||
			(strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".db"))
		if !leftover || e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, name)); err != nil {
			s.Log.Warn("interrupted instance backup not removed", "file", name, "err", err)
			continue
		}
		s.Log.Warn("removed remains of an interrupted instance backup", "file", name)
		n++
	}
	return n
}

// List returns all backups, newest first.
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Info{}, nil
		}
		return nil, err
	}
	out := []Info{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".tar.gz")
		if !ok || !idPattern.MatchString(id) {
			continue
		}
		info, err := s.info(id)
		if err != nil {
			s.Log.Warn("unreadable instance backup", "id", id, "err", err)
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) info(id string) (Info, error) {
	path, err := s.path(id)
	if err != nil {
		return Info{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Info{}, err
	}
	meta, err := readMeta(path)
	if err != nil {
		return Info{}, err
	}
	return Info{ID: id, Kind: idPattern.FindStringSubmatch(id)[1], SizeBytes: st.Size(), CreatedAt: meta.CreatedAt, Meta: meta}, nil
}

// Get returns one backup.
func (s *Store) Get(id string) (Info, error) {
	info, err := s.info(id)
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, ErrNotFound
	}
	return info, err
}

func (s *Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.Dir, id+".tar.gz"), nil
}

// readMeta reads instance.json, which is the first entry of every backup.
func readMeta(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Meta{}, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return Meta{}, fmt.Errorf("not a tar archive: %w", err)
	}
	if hdr.Name != metaFile {
		return Meta{}, fmt.Errorf("not an Envoryx instance backup (first entry is %q)", hdr.Name)
	}
	var meta Meta
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&meta); err != nil {
		return Meta{}, fmt.Errorf("read %s: %w", metaFile, err)
	}
	if meta.Format != format {
		return Meta{}, fmt.Errorf("unsupported backup format %d", meta.Format)
	}
	return meta, nil
}

// Open returns the archive for download.
func (s *Store) Open(id string) (io.ReadCloser, string, int64, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", 0, ErrNotFound
		}
		return nil, "", 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, "", 0, err
	}
	return f, "envoryx-" + id + ".tar.gz", st.Size(), nil
}

// Import stores an uploaded archive after checking that it is a readable instance backup
// this build can restore.
func (s *Store) Import(r io.Reader) (Info, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Info{}, fmt.Errorf("create instance backup directory: %w", err)
	}
	id := newID(KindUpload)
	target := filepath.Join(s.Dir, id+".tar.gz")
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return Info{}, err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(target)
		return Info{}, fmt.Errorf("store upload: %w", err)
	}
	meta, err := readMeta(target)
	if err != nil {
		_ = os.Remove(target)
		return Info{}, fmt.Errorf("%w: %v", validate.ErrInvalid, err)
	}
	if meta.Schema > s.LatestSchema {
		_ = os.Remove(target)
		return Info{}, fmt.Errorf("%w: the backup was made with a newer Envoryx (schema %d, this build supports %d)", validate.ErrInvalid, meta.Schema, s.LatestSchema)
	}
	return s.Get(id)
}

// Delete removes a backup. A backup with a scheduled restore cannot be deleted.
func (s *Store) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if p, _ := s.PendingRestore(); p != nil && p.ID == id {
		return ErrPending
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// prune keeps only the newest automatic backups of a kind; manual and uploaded ones are
// the user's business.
func (s *Store) prune(kind string) {
	if kind != KindPreMigrate && kind != KindPreRestore && kind != KindScheduled {
		return
	}
	list, err := s.List()
	if err != nil {
		return
	}
	n := 0
	for _, b := range list {
		if b.Kind != kind {
			continue
		}
		n++
		if n > keepAutomatic {
			if err := s.Delete(b.ID); err != nil {
				s.Log.Warn("could not prune instance backup", "id", b.ID, "err", err)
			}
		}
	}
}

// ---- restore ----------------------------------------------------------------------

func (s *Store) markerPath() string { return filepath.Join(s.ConfigDir, pendingMarker) }

// PendingRestore reports a scheduled restore, if any.
func (s *Store) PendingRestore() (*Pending, error) {
	raw, err := os.ReadFile(s.markerPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("read restore marker: %w", err)
	}
	return &p, nil
}

// ScheduleRestore records that id is to be restored at the next start. The caller is
// expected to restart the server afterwards.
func (s *Store) ScheduleRestore(id, requestedBy string) error {
	info, err := s.Get(id)
	if err != nil {
		return err
	}
	if info.Meta.Schema > s.LatestSchema {
		return fmt.Errorf("%w: the backup was made with a newer Envoryx (schema %d, this build supports %d)", validate.ErrInvalid, info.Meta.Schema, s.LatestSchema)
	}
	if p, err := s.PendingRestore(); err != nil {
		return err
	} else if p != nil {
		return ErrPending
	}
	raw, _ := json.Marshal(Pending{ID: id, RequestedAt: time.Now().UTC(), RequestedBy: requestedBy})
	return os.WriteFile(s.markerPath(), raw, 0o600)
}

// CancelRestore removes a scheduled restore.
func (s *Store) CancelRestore() error {
	err := os.Remove(s.markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ApplyPendingRestore performs a scheduled restore. It must run before the database is
// opened. The current state is saved as a pre-restore backup first; if the restore
// itself fails, that backup is the way back and its ID is part of the error.
// It returns what was restored, or nil when nothing was scheduled.
func (s *Store) ApplyPendingRestore(ctx context.Context, openDB func(context.Context, string) (*sql.DB, error)) (*Applied, error) {
	p, err := s.PendingRestore()
	if err != nil || p == nil {
		return nil, err
	}
	// The marker is consumed whatever happens; a failed restore must not loop.
	if err := s.CancelRestore(); err != nil {
		return nil, err
	}
	info, err := s.Get(p.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduled restore of %s: %w", p.ID, err)
	}
	if info.Meta.Schema > s.LatestSchema {
		return nil, fmt.Errorf("scheduled restore of %s: backup schema %d is newer than this build (%d)", p.ID, info.Meta.Schema, s.LatestSchema)
	}
	s.Log.Info("restoring instance backup", "id", p.ID, "created", info.CreatedAt, "envoryx", info.Meta.Envoryx)

	safety := ""
	if _, err := os.Stat(s.DBPath); err == nil {
		sqlDB, err := openDB(ctx, s.DBPath)
		if err != nil {
			return nil, fmt.Errorf("open database for pre-restore backup: %w", err)
		}
		pre, err := s.Create(ctx, sqlDB, KindPreRestore, "before restoring "+p.ID)
		_ = sqlDB.Close()
		if err != nil {
			return nil, fmt.Errorf("pre-restore backup: %w", err)
		}
		safety = pre.ID
		s.Log.Info("pre-restore backup written", "id", safety)
	}

	if err := s.extract(p.ID); err != nil {
		if safety != "" {
			return nil, fmt.Errorf("restore %s failed (the previous state is in backup %s): %w", p.ID, safety, err)
		}
		return nil, fmt.Errorf("restore %s failed: %w", p.ID, err)
	}
	return &Applied{Pending: *p, PreRestoreID: safety}, nil
}

// extract replaces the database and the archived parts of the config directory with the
// backup's content.
func (s *Store) extract(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := s.clearRestorable(); err != nil {
		return fmt.Errorf("clear config directory: %w", err)
	}
	f, err := os.Open(path)
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
	realRoot, err := filepath.EvalSymlinks(s.ConfigDir)
	if err != nil {
		return err
	}
	seenDB := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // backups contain regular files only
		}
		switch {
		case hdr.Name == metaFile:
			continue
		case hdr.Name == dbFile:
			if err := writeFile(s.DBPath, tr, 0o600); err != nil {
				return fmt.Errorf("restore database: %w", err)
			}
			seenDB = true
		case strings.HasPrefix(hdr.Name, configPrefix):
			rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(hdr.Name, configPrefix)))
			if filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("archive entry %q escapes the config directory", hdr.Name)
			}
			dest := filepath.Join(realRoot, rel)
			// The parent must resolve inside the root even through pre-existing symlinks.
			if parent, err := filepath.EvalSymlinks(filepath.Dir(dest)); err == nil && !within(parent, realRoot) {
				return fmt.Errorf("archive entry %q escapes the config directory", hdr.Name)
			}
			if err := writeFile(dest, tr, os.FileMode(hdr.Mode)&0o777); err != nil {
				return fmt.Errorf("restore %s: %w", rel, err)
			}
		default:
			return fmt.Errorf("unexpected archive entry %q", hdr.Name)
		}
	}
	if !seenDB {
		return errors.New("archive contains no database")
	}
	return nil
}

// clearRestorable removes everything a backup replaces: the database files and the
// archived config paths. Caches, project backups and the instance backups stay.
func (s *Store) clearRestorable() error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(s.DBPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	entries, err := os.ReadDir(s.ConfigDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(s.ConfigDir, e.Name())
		if s.skip(path, e.Name(), e.IsDir()) {
			continue
		}
		if e.Name() == "projects" && e.IsDir() {
			// Per-project directories: drop the generated files, keep the home caches.
			projects, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, p := range projects {
				dir := filepath.Join(path, p.Name())
				items, err := os.ReadDir(dir)
				if err != nil {
					if err := os.RemoveAll(dir); err != nil {
						return err
					}
					continue
				}
				keep := false
				for _, it := range items {
					if it.IsDir() && it.Name() == "home" {
						keep = true
						continue
					}
					if err := os.RemoveAll(filepath.Join(dir, it.Name())); err != nil {
						return err
					}
				}
				if !keep {
					if err := os.Remove(dir); err != nil {
						return err
					}
				}
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(dest string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o600
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

func schemaVersion(ctx context.Context, sqlDB *sql.DB) (int, error) {
	var v sql.NullInt64
	err := sqlDB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return 0, nil
	}
	return int(v.Int64), err
}

func newID(kind string) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return kind + "-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

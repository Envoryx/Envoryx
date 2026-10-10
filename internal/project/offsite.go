package project

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Offsite targets receive a backup as the same tar the download produces and hand it
// back the same way; the offsite package does the transport, this file the two ends.

// backupArchiveMembers are the files of a backup directory, in archive order (the
// metadata first, so a reader knows early what it holds).
var backupArchiveMembers = []string{backupMetaFile, backupDBFile, backupFilesFile, backupStorageFile}

// extraDumpRe matches the dump files of additional databases (database-<name>.sql.gz).
var extraDumpRe = regexp.MustCompile(`^database-([a-z][a-z0-9-]*)\.sql\.gz$`)

// dbVolumeCopyRe matches the copies of database data volumes
// (database.volume.tar.gz, database-<name>.volume.tar.gz).
var dbVolumeCopyRe = regexp.MustCompile(`^database(?:-([a-z][a-z0-9-]*))?\.volume\.tar\.gz$`)

// addonVolumeRe matches the archives of addon volumes (addon-<name>-<volume>.tar.gz, see
// addonBackupFile); addon and volume names are lower case letters, digits and dashes.
var addonVolumeRe = regexp.MustCompile(`^addon-[a-z][a-z0-9-]*[a-z0-9]\.tar\.gz$`)

// isBackupMember reports a file name that belongs in a backup directory.
func isBackupMember(name string) bool {
	if slices.Contains(backupArchiveMembers, name) || addonVolumeRe.MatchString(name) {
		return true
	}
	if m := dbVolumeCopyRe.FindStringSubmatch(name); m != nil {
		return m[1] == "" || ValidateDatabaseServiceName(m[1]) == nil
	}
	m := extraDumpRe.FindStringSubmatch(name)
	return m != nil && ValidateDatabaseServiceName(m[1]) == nil
}

// backupMembersIn lists the files of a backup directory in archive order: the fixed
// members, the dumps of additional databases right after the primary's.
func backupMembersIn(dir string) []string {
	var extras []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !slices.Contains(backupArchiveMembers, e.Name()) && isBackupMember(e.Name()) {
				extras = append(extras, e.Name())
			}
		}
	}
	sort.Strings(extras)
	out := []string{backupMetaFile, backupDBFile}
	out = append(out, extras...)
	return append(out, backupFilesFile, backupStorageFile)
}

// SetBackupHook registers a function told about every backup created.
func (m *Manager) SetBackupHook(f func(projectID string, b BackupInfo)) { m.backupHook = f }

// OffsiteArchive is a backup as a stream plus what names it on a target.
type OffsiteArchive struct {
	Slug   string
	Dir    string
	Kind   string
	Source string
	Body   io.ReadCloser
}

// OpenOffsiteArchive streams a backup for an upload.
func (m *Manager) OpenOffsiteArchive(ctx context.Context, projectID, backupID string) (OffsiteArchive, error) {
	rc, _, err := m.OpenBackupArchive(ctx, projectID, backupID)
	if err != nil {
		return OffsiteArchive{}, err
	}
	p, err := m.store.Projects.Get(ctx, projectID)
	if err != nil {
		rc.Close()
		return OffsiteArchive{}, err
	}
	b, err := m.store.Backups.Get(ctx, projectID, backupID)
	if err != nil {
		rc.Close()
		return OffsiteArchive{}, err
	}
	var meta BackupMeta
	_ = json.Unmarshal(b.Metadata, &meta)
	source := meta.Source
	if source == "" {
		source = "manual"
	}
	return OffsiteArchive{Slug: p.Slug, Dir: b.Filename, Kind: b.Kind, Source: source, Body: rc}, nil
}

// ProjectSlug returns the identifier offsite copies of a project are filed under.
func (m *Manager) ProjectSlug(ctx context.Context, projectID string) (string, error) {
	if err := validate.UUID(projectID); err != nil {
		return "", ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, projectID)
	if err != nil {
		return "", err
	}
	return p.Slug, nil
}

// ImportBackupArchive stores a backup tar (as OpenBackupArchive writes it) as a local
// backup of the project and records it. The backup must belong to the project - the
// same id, or the same identifier after the project was recreated elsewhere. A backup
// that is already there is returned as it is.
func (m *Manager) ImportBackupArchive(ctx context.Context, projectID string, r io.Reader) (BackupInfo, error) {
	if err := validate.UUID(projectID); err != nil {
		return BackupInfo{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, projectID)
	if err != nil {
		return BackupInfo{}, err
	}
	return m.storeBackupArchive(ctx, r, func(bf backupFile) (backupOwner, error) {
		if bf.ProjectID != p.ID && bf.Slug != p.Slug {
			return backupOwner{}, fmt.Errorf("%w: the backup belongs to project %q, not %q", validate.ErrInvalid, bf.ProjectName, p.Name)
		}
		return backupOwner{ID: p.ID, Slug: p.Slug, Name: p.Name}, nil
	})
}

// backupOwner is the project a stored backup is recorded for: one that exists, or the
// deleted project the backup was made of.
type backupOwner struct{ ID, Slug, Name string }

// storeBackupArchive unpacks a backup tar, lets owner decide whose backup it becomes,
// and records it below that project's backup directory.
func (m *Manager) storeBackupArchive(ctx context.Context, r io.Reader, owner func(backupFile) (backupOwner, error)) (BackupInfo, error) {
	paths, err := m.paths()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	if err := os.MkdirAll(paths.BackupsRoot(), 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup directory: %w", err)
	}
	// Next to the projects' backup directories, under a name no slug can take.
	tmp, err := os.MkdirTemp(paths.BackupsRoot(), ".import-")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.RemoveAll(tmp)
	dirName, bf, err := unpackBackupArchive(r, tmp)
	if err != nil {
		return BackupInfo{}, err
	}
	p, err := owner(bf)
	if err != nil {
		return BackupInfo{}, err
	}
	root, err := m.backupRoot(p.Slug)
	if err != nil {
		return BackupInfo{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup directory: %w", err)
	}
	if rows, err := m.store.Backups.ListByProject(ctx, p.ID); err == nil {
		for _, b := range rows {
			if b.Filename != dirName {
				continue
			}
			var meta BackupMeta
			_ = json.Unmarshal(b.Metadata, &meta)
			if _, err := m.locateBackup(ctx, b, meta); err == nil {
				return BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}, nil
			}
			// Recorded but the files are gone: bring them back under the same record.
			target := filepath.Join(root, dirName)
			_ = os.RemoveAll(target)
			if err := os.Rename(tmp, target); err != nil {
				return BackupInfo{}, err
			}
			return BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}, nil
		}
	}
	target := filepath.Join(root, dirName)
	if _, err := os.Stat(target); err == nil {
		return BackupInfo{}, fmt.Errorf("%w: a backup directory %s exists already", store.ErrConflict, dirName)
	}
	if err := os.Rename(tmp, target); err != nil {
		return BackupInfo{}, err
	}
	kind := backupKind(BackupOptions{Database: bf.HasAnyDatabase(), Files: bf.Files != nil, Storage: bf.Storage != nil})
	metaJSON, _ := json.Marshal(bf.BackupMeta)
	rec := &store.Backup{ProjectID: p.ID, Filename: dirName, SizeBytes: dirSize(target), Kind: kind, Metadata: metaJSON, CreatedAt: bf.CreatedAt}
	if err := m.store.Backups.Create(ctx, rec); err != nil {
		_ = os.RemoveAll(target)
		return BackupInfo{}, err
	}
	m.audit.Log(ctx, audit.ActionBackupCreated, "project", p.ID, map[string]any{"name": p.Name, "backup": rec.ID, "kind": kind, "bytes": rec.SizeBytes, "imported": true})
	return BackupInfo{ID: rec.ID, Dir: dirName, Kind: kind, SizeBytes: rec.SizeBytes, CreatedAt: rec.CreatedAt, Meta: bf.BackupMeta}, nil
}

// notABackup marks an archive that isn't an Envoryx backup.
func notABackup(format string, a ...any) error {
	return fmt.Errorf("%w: not an Envoryx backup: %s", validate.ErrInvalid, fmt.Sprintf(format, a...))
}

// unpackBackupArchive writes the members of a backup tar into dir and returns the
// backup's directory name and its backup.json.
func unpackBackupArchive(r io.Reader, dir string) (string, backupFile, error) {
	tr := tar.NewReader(r)
	dirName := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", backupFile{}, notABackup("%v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", backupFile{}, notABackup("unexpected entry %q", hdr.Name)
		}
		top, name := path.Split(hdr.Name)
		top = strings.TrimSuffix(top, "/")
		if strings.Contains(top, "/") || !isBackupMember(name) {
			return "", backupFile{}, notABackup("unexpected entry %q", hdr.Name)
		}
		// The top directory is <slug>-<backup dir>; the backup dir has a fixed shape.
		if len(top) < 24 || !backupDirPattern.MatchString(top[len(top)-24:]) {
			return "", backupFile{}, notABackup("unexpected directory %q", top)
		}
		if dirName == "" {
			dirName = top[len(top)-24:]
		} else if top[len(top)-24:] != dirName {
			return "", backupFile{}, notABackup("entries of more than one backup")
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return "", backupFile{}, err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", backupFile{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, backupMetaFile))
	if err != nil {
		return "", backupFile{}, notABackup("backup.json is missing")
	}
	var bf backupFile
	if err := json.Unmarshal(raw, &bf); err != nil {
		return "", backupFile{}, notABackup("backup.json is unreadable")
	}
	return dirName, bf, nil
}

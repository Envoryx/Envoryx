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
	"slices"
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
// backup of the project and records it. The backup must belong to the project – the
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
	root, err := m.backupRoot(p.Slug)
	if err != nil {
		return BackupInfo{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup directory: %w", err)
	}
	tmp, err := os.MkdirTemp(root, ".import-")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.RemoveAll(tmp)

	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: not an Envoryx backup: %s", validate.ErrInvalid, fmt.Sprintf(format, a...))
	}
	tr := tar.NewReader(r)
	dirName := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return BackupInfo{}, bad("%v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return BackupInfo{}, bad("unexpected entry %q", hdr.Name)
		}
		top, name := path.Split(hdr.Name)
		top = strings.TrimSuffix(top, "/")
		if strings.Contains(top, "/") || !slices.Contains(backupArchiveMembers, name) {
			return BackupInfo{}, bad("unexpected entry %q", hdr.Name)
		}
		// The top directory is <slug>-<backup dir>; the backup dir has a fixed shape.
		if len(top) < 24 || !backupDirPattern.MatchString(top[len(top)-24:]) {
			return BackupInfo{}, bad("unexpected directory %q", top)
		}
		if dirName == "" {
			dirName = top[len(top)-24:]
		} else if top[len(top)-24:] != dirName {
			return BackupInfo{}, bad("entries of more than one backup")
		}
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return BackupInfo{}, err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return BackupInfo{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(tmp, backupMetaFile))
	if err != nil {
		return BackupInfo{}, bad("backup.json is missing")
	}
	var bf backupFile
	if err := json.Unmarshal(raw, &bf); err != nil {
		return BackupInfo{}, bad("backup.json is unreadable")
	}
	if bf.ProjectID != p.ID && bf.Slug != p.Slug {
		return BackupInfo{}, fmt.Errorf("%w: the backup belongs to project %q, not %q", validate.ErrInvalid, bf.ProjectName, p.Name)
	}
	if rows, err := m.store.Backups.ListByProject(ctx, p.ID); err == nil {
		for _, b := range rows {
			if b.Filename == dirName {
				list, err := m.ListBackups(ctx, p.ID)
				if err == nil {
					for _, info := range list {
						if info.ID == b.ID && !info.Missing {
							return info, nil
						}
					}
				}
				// Recorded but the files are gone: bring them back under the same record.
				target := filepath.Join(root, dirName)
				_ = os.RemoveAll(target)
				if err := os.Rename(tmp, target); err != nil {
					return BackupInfo{}, err
				}
				var meta BackupMeta
				_ = json.Unmarshal(b.Metadata, &meta)
				return BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Meta: meta}, nil
			}
		}
	}
	target := filepath.Join(root, dirName)
	if _, err := os.Stat(target); err == nil {
		return BackupInfo{}, fmt.Errorf("%w: a backup directory %s exists already", store.ErrConflict, dirName)
	}
	if err := os.Rename(tmp, target); err != nil {
		return BackupInfo{}, err
	}
	kind := backupKind(BackupOptions{Database: bf.Database != nil, Files: bf.Files != nil, Storage: bf.Storage != nil})
	metaJSON, _ := json.Marshal(bf.BackupMeta)
	rec := &store.Backup{ProjectID: p.ID, Filename: dirName, SizeBytes: dirSize(target), Kind: kind, Metadata: metaJSON, CreatedAt: bf.CreatedAt}
	if err := m.store.Backups.Create(ctx, rec); err != nil {
		_ = os.RemoveAll(target)
		return BackupInfo{}, err
	}
	m.audit.Log(ctx, audit.ActionBackupCreated, "project", p.ID, map[string]any{"name": p.Name, "backup": rec.ID, "kind": kind, "bytes": rec.SizeBytes, "imported": true})
	return BackupInfo{ID: rec.ID, Dir: dirName, Kind: kind, SizeBytes: rec.SizeBytes, CreatedAt: rec.CreatedAt, Meta: bf.BackupMeta}, nil
}

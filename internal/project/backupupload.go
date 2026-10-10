package project

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// A downloaded backup can come back through the site import of the project wizard: the
// archive is recognised there, stored as a backup of its project (or of the deleted
// project it was made of) and restored into a new project from that record.

// UploadedBackup is a backup archive uploaded instead of a website, now a backup on this
// instance.
type UploadedBackup struct {
	OrphanedBackup
	// ProjectExists tells that the backup's project is on this instance; the backup is
	// one of its backups now. Otherwise it is listed with the backups of deleted projects.
	ProjectExists bool `json:"projectExists"`
}

// IsBackupArchive reports whether file is a backup as the download writes it (a tar,
// possibly gzipped, whose first member is <slug>-<backup dir>/backup.json).
func IsBackupArchive(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	r, closeR, err := openMaybeGzip(f)
	if err != nil {
		return false
	}
	defer closeR()
	hdr, err := tar.NewReader(r).Next()
	if err != nil || hdr.Typeflag != tar.TypeReg {
		return false
	}
	top, name := path.Split(hdr.Name)
	top = strings.TrimSuffix(top, "/")
	return name == backupMetaFile && !strings.Contains(top, "/") && len(top) >= 24 && backupDirPattern.MatchString(top[len(top)-24:])
}

// openMaybeGzip reads r, unpacking it when it is gzipped.
func openMaybeGzip(r io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(2); len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, notABackup("%v", err)
		}
		return gz, func() { _ = gz.Close() }, nil
	}
	return br, func() {}, nil
}

// ImportBackupFile stores an uploaded backup archive as a backup of its project: of the
// project it was made of when that is on this instance, otherwise of the deleted project,
// whose backups can be restored into a new project as well. Its project settings must be
// readable here, since a restore into a new project is made from them.
func (m *Manager) ImportBackupFile(ctx context.Context, file string) (UploadedBackup, error) {
	f, err := os.Open(file)
	if err != nil {
		return UploadedBackup{}, err
	}
	defer f.Close()
	r, closeR, err := openMaybeGzip(f)
	if err != nil {
		return UploadedBackup{}, err
	}
	defer closeR()
	var out UploadedBackup
	info, err := m.storeBackupArchive(ctx, r, func(bf backupFile) (backupOwner, error) {
		if validate.UUID(bf.ProjectID) != nil || validate.Slug(bf.Slug) != nil {
			return backupOwner{}, notABackup("backup.json names no project")
		}
		if _, err := bf.export(); err != nil {
			return backupOwner{}, err
		}
		owner := backupOwner{ID: bf.ProjectID, Slug: bf.Slug, Name: bf.ProjectName}
		if p, err := m.store.Projects.Get(ctx, bf.ProjectID); err == nil {
			owner = backupOwner{ID: p.ID, Slug: p.Slug, Name: p.Name}
			out.ProjectExists = true
		}
		out.ProjectID, out.ProjectName, out.Slug = owner.ID, owner.Name, owner.Slug
		return owner, nil
	})
	if err != nil {
		return UploadedBackup{}, err
	}
	out.Backup = info
	return out, nil
}

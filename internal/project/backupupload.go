package project

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
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

// IsBackupArchive reports whether file is a backup: a tar, possibly gzipped, of one
// <slug>-<backup dir> folder with the backup's files. The download writes backup.json
// first; a folder unpacked and packed again by hand (tar cf x.tar <folder>) starts with
// the folder's own entry and has the files in any order, which counts as well.
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
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return false
		}
		if _, ok := backupFolderEntry(hdr); ok {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return false
		}
		top, name, ok := splitBackupMember(hdr.Name)
		return ok && isBackupMember(name) && backupFolder(top) != ""
	}
}

// splitBackupMember splits a member's path into its top folder and file name; ok is
// false for anything deeper than one folder. A leading "./" (tar cf x.tar ./folder) is
// ignored.
func splitBackupMember(member string) (top, name string, ok bool) {
	top, name = path.Split(strings.TrimPrefix(member, "./"))
	top = strings.TrimSuffix(top, "/")
	return top, name, top != "" && !strings.Contains(top, "/")
}

// backupFolder is the backup directory a top folder <slug>-<backup dir> names, "" when
// it has another shape.
func backupFolder(top string) string {
	if len(top) < 24 || !backupDirPattern.MatchString(top[len(top)-24:]) {
		return ""
	}
	return top[len(top)-24:]
}

// backupFolderEntry reports whether hdr is the entry of the top folder itself, which a
// folder packed by hand carries; it returns the backup directory the folder names.
func backupFolderEntry(hdr *tar.Header) (string, bool) {
	if hdr.Typeflag != tar.TypeDir {
		return "", false
	}
	top := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
	if top == "" || top == "." {
		return "", true
	}
	if strings.Contains(top, "/") {
		return "", false
	}
	dir := backupFolder(top)
	return dir, dir != ""
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

// ImportBackupFile stores an uploaded backup archive (a tar, possibly gzipped) as a
// backup of its project; see ImportBackupStream.
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
	return m.ImportBackupStream(ctx, r)
}

// ImportBackupStream stores a backup tar as a backup of its project: of the project it
// was made of when that is on this instance, otherwise of the deleted project, whose
// backups can be restored into a new project as well. Its project settings must be
// readable here, since a restore into a new project is made from them, or sealed with
// another Envoryx's key (BackupInfo.SealedWith), which the restore then asks for.
func (m *Manager) ImportBackupStream(ctx context.Context, r io.Reader) (UploadedBackup, error) {
	var out UploadedBackup
	info, err := m.storeBackupArchive(ctx, r, func(bf backupFile) (backupOwner, error) {
		if validate.UUID(bf.ProjectID) != nil || validate.Slug(bf.Slug) != nil {
			return backupOwner{}, notABackup("backup.json names no project")
		}
		if bf.foreignKey() == "" {
			if _, err := bf.export(); err != nil {
				return backupOwner{}, err
			}
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
	// Uploaded once more, the backup is the one already here, which a restore may have
	// sealed with this Envoryx's key since.
	if b, err := m.store.Backups.Get(ctx, out.ProjectID, info.ID); err == nil {
		if dir, err := m.locateBackup(ctx, b, info.Meta); err == nil {
			out.Backup.SealedWith, _ = foreignKeyOf(filepath.Join(dir, backupMetaFile))
		}
	}
	return out, nil
}

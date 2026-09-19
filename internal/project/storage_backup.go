package project

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/store"
)

// Object storage goes into backups logically, like the database: every object becomes
// a file in storage.tar.gz named by its key, with the content type kept as an xattr record.
// That keeps the archive readable with any tar and independent of the server's on-disk
// format, and a restore is a plain upload.
const (
	backupStorageFile = "storage.tar.gz"
	// paxContentType is the PAX record carrying the content type: the xattr namespace tar
	// implementations know, with the mime-type attribute name desktop tools use.
	paxContentType = "SCHILY.xattr.user.mime_type"
)

// storageStore returns an object store for the project's running storage container.
func (m *Manager) storageStore(ctx context.Context, p store.Project) (s3.ObjectStore, runtime.StorageConfig, error) {
	_, cfg, err := storageConfig(p)
	if err != nil {
		return nil, cfg, err
	}
	c, err := m.ServiceContainer(ctx, p.ID, store.ServiceStorage)
	if err != nil {
		return nil, cfg, err
	}
	if c.State != "running" {
		return nil, cfg, fmt.Errorf("%w: the object storage container must be running", ErrConflict)
	}
	paths, err := m.paths()
	if err != nil {
		return nil, cfg, err
	}
	endpoint := m.storageDial(paths.SelfContainerID, p, cfg)
	if endpoint == "" {
		return nil, cfg, errors.New("object storage: no published port to reach the server from the host")
	}
	if m.objectStore != nil {
		return m.objectStore("http://"+endpoint, cfg.AccessKey, cfg.SecretKey), cfg, nil
	}
	return s3.NewClient("http://"+endpoint, cfg.AccessKey, cfg.SecretKey), cfg, nil
}

// dumpStorage writes every object of the bucket into target and returns object count
// and total bytes.
func (m *Manager) dumpStorage(ctx context.Context, p store.Project, target string) (int, int64, error) {
	st, cfg, err := m.storageStore(ctx, p)
	if err != nil {
		return 0, 0, err
	}
	objects, err := st.ListObjects(ctx, cfg.Bucket)
	if err != nil {
		return 0, 0, fmt.Errorf("list objects: %w", err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var total int64
	write := func() error {
		for _, o := range objects {
			body, ctype, err := st.GetObject(ctx, cfg.Bucket, o.Key)
			if err != nil {
				return fmt.Errorf("get %s: %w", o.Key, err)
			}
			hdr := &tar.Header{Name: o.Key, Mode: 0o644, Size: o.Size, ModTime: time.Now().UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
			if ctype != "" {
				hdr.PAXRecords = map[string]string{paxContentType: ctype}
			}
			if err := tw.WriteHeader(hdr); err != nil {
				body.Close()
				return err
			}
			n, err := io.Copy(tw, body)
			body.Close()
			if err != nil {
				return fmt.Errorf("read %s: %w", o.Key, err)
			}
			if n != o.Size {
				return fmt.Errorf("read %s: got %d bytes, listing said %d", o.Key, n, o.Size)
			}
			total += n
		}
		return nil
	}
	werr := write()
	for _, c := range []io.Closer{tw, gz, f} {
		if err := c.Close(); err != nil && werr == nil {
			werr = err
		}
	}
	if werr != nil {
		_ = os.Remove(target)
		return 0, 0, werr
	}
	return len(objects), total, nil
}

// restoreStorage uploads the archive's objects into the bucket, optionally emptying it
// first. Keys are taken as archived; anything that is not a plain relative path is
// skipped.
func (m *Manager) restoreStorage(ctx context.Context, p store.Project, archive string, wipe bool) error {
	st, cfg, err := m.storageStore(ctx, p)
	if err != nil {
		return err
	}
	if wipe {
		existing, err := st.ListObjects(ctx, cfg.Bucket)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		keys := make([]string, 0, len(existing))
		for _, o := range existing {
			keys = append(keys, o.Key)
		}
		if len(keys) > 0 {
			if err := st.DeleteObjects(ctx, cfg.Bucket, keys); err != nil {
				return fmt.Errorf("empty bucket: %w", err)
			}
		}
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !validObjectKey(hdr.Name) {
			continue
		}
		ctype := hdr.PAXRecords[paxContentType]
		if ctype == "" {
			ctype = mime.TypeByExtension(path.Ext(hdr.Name))
		}
		if err := st.PutObject(ctx, cfg.Bucket, hdr.Name, tr, hdr.Size, ctype); err != nil {
			return fmt.Errorf("upload %s: %w", hdr.Name, err)
		}
	}
}

// validObjectKey accepts relative keys without traversal or control characters.
func validObjectKey(k string) bool {
	if k == "" || len(k) > 1024 || strings.HasPrefix(k, "/") || strings.Contains(k, "\x00") {
		return false
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

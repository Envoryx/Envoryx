package siteimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// TTL is how long an upload waits for the project it was made for.
const TTL = 24 * time.Hour

// Staged is an uploaded site waiting to become a project.
type Staged struct {
	ID        string    `json:"id"`
	SiteName  string    `json:"siteName"`
	DumpName  string    `json:"dumpName,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Analysis  Analysis  `json:"analysis"`

	dir string
}

// SiteFile is the uploaded archive.
func (s Staged) SiteFile() string { return filepath.Join(s.dir, "site") }

// DumpFile is the uploaded dump ("" = none).
func (s Staged) DumpFile() string {
	if s.DumpName == "" {
		return ""
	}
	return filepath.Join(s.dir, "dump")
}

// Staging keeps uploads in a directory of their own, one folder each.
type Staging struct {
	Dir string
	Now func() time.Time
}

func (st Staging) now() time.Time {
	if st.Now != nil {
		return st.Now()
	}
	return time.Now()
}

// Upload is a staging folder being filled.
type Upload struct {
	st       Staging
	id, dir  string
	siteName string
	dumpName string
}

var uploadNameRe = regexp.MustCompile(`[^A-Za-z0-9._ ()+-]`)

func cleanUploadName(name string) string {
	name = uploadNameRe.ReplaceAllString(filepath.Base(filepath.ToSlash(name)), "_")
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// Begin starts a new upload; expired ones are cleared first.
func (st Staging) Begin() (*Upload, error) {
	st.Prune()
	if err := os.MkdirAll(st.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the upload directory: %w", err)
	}
	id := store.NewID()
	dir := filepath.Join(st.Dir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	return &Upload{st: st, id: id, dir: dir}, nil
}

func (u *Upload) save(name string, r io.Reader) error {
	f, err := os.OpenFile(filepath.Join(u.dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Site stores the site archive.
func (u *Upload) Site(r io.Reader, filename string) error {
	u.siteName = cleanUploadName(filename)
	if u.siteName == "" || u.siteName == "." {
		u.siteName = "site"
	}
	return u.save("site", r)
}

// Dump stores the database dump.
func (u *Upload) Dump(r io.Reader, filename string) error {
	u.dumpName = cleanUploadName(filename)
	if u.dumpName == "" || u.dumpName == "." {
		u.dumpName = "dump.sql"
	}
	return u.save("dump", r)
}

// Finish analyses what was uploaded and keeps it for the project to come. A failed
// analysis removes the upload.
func (u *Upload) Finish(opt Options) (Staged, error) {
	if u.siteName == "" {
		u.Abort()
		return Staged{}, fmt.Errorf("%w: the website archive is missing", validate.ErrInvalid)
	}
	st := Staged{ID: u.id, SiteName: u.siteName, DumpName: u.dumpName, CreatedAt: u.st.now().UTC(), dir: u.dir}
	st.ExpiresAt = st.CreatedAt.Add(TTL)
	a, err := Analyze(st.SiteFile(), st.DumpFile(), opt)
	if err != nil {
		u.Abort()
		if !errors.Is(err, validate.ErrInvalid) {
			err = fmt.Errorf("%w: %v", validate.ErrInvalid, err)
		}
		return Staged{}, err
	}
	st.Analysis = a
	raw, err := json.Marshal(st)
	if err != nil {
		u.Abort()
		return Staged{}, err
	}
	if err := os.WriteFile(filepath.Join(u.dir, "staged.json"), raw, 0o600); err != nil {
		u.Abort()
		return Staged{}, err
	}
	return st, nil
}

// Abort removes the upload.
func (u *Upload) Abort() { _ = os.RemoveAll(u.dir) }

// Get returns an upload that has not expired.
func (st Staging) Get(id string) (Staged, error) {
	if validate.UUID(id) != nil {
		return Staged{}, fmt.Errorf("%w: unknown website upload", store.ErrNotFound)
	}
	dir := filepath.Join(st.Dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, "staged.json"))
	if err != nil {
		return Staged{}, fmt.Errorf("%w: the website upload is gone (uploads are kept for %s); upload it again", store.ErrNotFound, TTL)
	}
	var s Staged
	if err := json.Unmarshal(raw, &s); err != nil {
		return Staged{}, err
	}
	s.dir = dir
	if st.now().After(s.ExpiresAt) {
		_ = os.RemoveAll(dir)
		return Staged{}, fmt.Errorf("%w: the website upload expired; upload it again", store.ErrNotFound)
	}
	return s, nil
}

// Remove discards an upload.
func (st Staging) Remove(id string) error {
	if validate.UUID(id) != nil {
		return fmt.Errorf("%w: unknown website upload", store.ErrNotFound)
	}
	return os.RemoveAll(filepath.Join(st.Dir, id))
}

// Prune removes expired uploads and the remains of interrupted ones.
func (st Staging) Prune() {
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		return
	}
	cutoff := st.now().Add(-TTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() || validate.UUID(e.Name()) != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(st.Dir, e.Name()))
		}
	}
}

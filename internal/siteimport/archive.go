// Package siteimport turns an existing website – an archive of its files, optionally a
// database dump – into the ingredients of a new project: it recognises what the site is
// (WordPress, Laravel, a plain PHP site…), suggests a runtime, and adapts the site's own
// configuration to the project database.
package siteimport

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// Format is the container format of an uploaded site archive.
type Format string

const (
	FormatZip   Format = "zip"
	FormatTarGz Format = "tar.gz"
	FormatTar   Format = "tar"
)

// MaxExtractBytes bounds what an archive may unpack to (a ZIP bomb stops there).
var MaxExtractBytes int64 = 64 << 30

// sniffFormat recognises the archive by its first bytes, not by its name.
func sniffFormat(file string) (Format, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return FormatZip, nil
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return FormatTarGz, nil
	case n >= 262 && string(head[257:262]) == "ustar":
		return FormatTar, nil
	}
	return "", fmt.Errorf("%w: the website must be a ZIP or tar archive (.zip, .tar.gz, .tgz, .tar)", validate.ErrInvalid)
}

type entryKind int

const (
	entryFile entryKind = iota
	entryDir
	entrySymlink
	entryOther
)

// entry is one member of an archive, whatever the format.
type entry struct {
	Name string // slash-separated and cleaned, no leading "./"
	Kind entryKind
	Mode fs.FileMode
	Size int64
	Link string // symlink target
	// open reads a file's contents; only valid during the walk callback.
	open func() (io.Reader, error)
}

// skipped reports members that only packing tools add (macOS resource forks, Finder files).
func skipped(name string) bool {
	return name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
}

// cleanName normalises a member name. ok is false for names that leave the archive root
// (absolute, "..") – those are refused, never extracted.
func cleanName(raw string) (name string, ok bool) {
	n := strings.TrimPrefix(raw, "./")
	if n == "" || n == "." {
		return "", true
	}
	if strings.HasPrefix(n, "/") || filepath.IsAbs(n) {
		return "", false
	}
	n = path.Clean(n)
	if n == ".." || strings.HasPrefix(n, "../") {
		return "", false
	}
	return n, true
}

// walk calls fn for every member of the archive in the order it is stored.
func walk(file string, format Format, fn func(e entry) error) error {
	escape := func(raw string) error {
		return fmt.Errorf("%w: archive entry %q escapes the archive", validate.ErrInvalid, raw)
	}
	switch format {
	case FormatZip:
		zr, err := zip.OpenReader(file)
		if err != nil {
			return fmt.Errorf("%w: read ZIP archive: %v", validate.ErrInvalid, err)
		}
		defer zr.Close()
		for _, f := range zr.File {
			// ZIP files packed on Windows sometimes separate with backslashes.
			raw := strings.ReplaceAll(f.Name, `\`, "/")
			name, ok := cleanName(raw)
			if !ok {
				return escape(f.Name)
			}
			if name == "" || skipped(name) {
				continue
			}
			mode := f.Mode()
			e := entry{Name: name, Mode: mode.Perm(), Size: int64(f.UncompressedSize64)}
			switch {
			case mode.IsDir() || strings.HasSuffix(raw, "/"):
				e.Kind = entryDir
			case mode&fs.ModeSymlink != 0:
				e.Kind = entrySymlink
				rc, err := f.Open()
				if err != nil {
					return err
				}
				target, err := io.ReadAll(io.LimitReader(rc, 4096))
				_ = rc.Close()
				if err != nil {
					return err
				}
				e.Link = string(target)
			case mode.IsRegular():
				e.Kind = entryFile
				var rc io.ReadCloser
				e.open = func() (io.Reader, error) {
					var err error
					rc, err = f.Open()
					return rc, err
				}
				err := fn(e)
				if rc != nil {
					_ = rc.Close()
				}
				if err != nil {
					return err
				}
				continue
			default:
				e.Kind = entryOther
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	case FormatTarGz, FormatTar:
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		var r io.Reader = f
		if format == FormatTarGz {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return fmt.Errorf("%w: read gzip archive: %v", validate.ErrInvalid, err)
			}
			defer gz.Close()
			r = gz
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%w: read tar archive: %v", validate.ErrInvalid, err)
			}
			name, ok := cleanName(hdr.Name)
			if !ok {
				return escape(hdr.Name)
			}
			if name == "" || skipped(name) {
				continue
			}
			e := entry{Name: name, Mode: fs.FileMode(hdr.Mode).Perm(), Size: hdr.Size}
			switch hdr.Typeflag {
			case tar.TypeDir:
				e.Kind = entryDir
			case tar.TypeSymlink:
				e.Kind, e.Link = entrySymlink, hdr.Linkname
			case tar.TypeReg:
				e.Kind = entryFile
				e.open = func() (io.Reader, error) { return tr, nil }
			case tar.TypeXGlobalHeader:
				continue
			default:
				e.Kind = entryOther
			}
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("unknown archive format %q", format)
}

// commonRoot returns the directory prefix every member shares ("public_html/" when the
// archive holds that one folder) – the prefix extraction strips. Single folders nested
// in each other are stripped together ("backup/httpdocs/").
func commonRoot(names []string) string {
	prefix := ""
	for depth := 0; depth < 4; depth++ {
		top := ""
		nested := false
		for _, n := range names {
			rest, ok := strings.CutPrefix(n, prefix)
			if !ok || rest == "" {
				continue
			}
			first, after, hasSlash := strings.Cut(rest, "/")
			if top == "" {
				top = first
			} else if first != top {
				return prefix
			}
			if hasSlash && after != "" {
				nested = true
			}
		}
		// A single file at the top is the whole site, not a folder to strip.
		if top == "" || !nested {
			return prefix
		}
		prefix += top + "/"
	}
	return prefix
}

// Extract unpacks the archive into target, leaving out strip (see commonRoot). Member
// names must stay inside target, symlinks may not point outside it, nothing is written
// through a symlink, and the whole may not unpack to more than MaxExtractBytes. Hard
// links, devices and fifos are skipped. Files belong to uid:gid when running as root.
func Extract(file, target, strip string, uid, gid int) error {
	format, err := sniffFormat(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	chown := func(p string) {
		if os.Geteuid() == 0 {
			_ = os.Lchown(p, uid, gid)
		}
	}
	inside := func(p string) bool {
		rel, err := filepath.Rel(realTarget, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	budget := MaxExtractBytes
	return walk(file, format, func(e entry) error {
		rel, ok := strings.CutPrefix(e.Name, strip)
		if !ok || rel == "" {
			return nil
		}
		dest := filepath.Join(realTarget, filepath.FromSlash(rel))
		// The parent must resolve inside the target even if an earlier symlink was extracted:
		// the deepest part of it that exists is resolved before anything is created below.
		existing := filepath.Dir(dest)
		for len(existing) > len(realTarget) {
			if _, err := os.Lstat(existing); err == nil {
				break
			}
			existing = filepath.Dir(existing)
		}
		resolved, err := filepath.EvalSymlinks(existing)
		if err != nil {
			return err
		}
		if !inside(resolved) {
			return fmt.Errorf("%w: archive entry %q escapes the project directory", validate.ErrInvalid, e.Name)
		}
		if err := mkdirAll(filepath.Dir(dest), realTarget, chown); err != nil {
			return err
		}
		switch e.Kind {
		case entryDir:
			if err := os.MkdirAll(dest, e.Mode|0o700); err != nil {
				return err
			}
			chown(dest)
		case entrySymlink:
			linkDest := e.Link
			if !filepath.IsAbs(linkDest) {
				linkDest = filepath.Join(filepath.Dir(dest), linkDest)
			}
			if !inside(filepath.Clean(linkDest)) {
				return nil // links pointing outside the project are left out
			}
			_ = os.RemoveAll(dest)
			if err := os.Symlink(e.Link, dest); err != nil {
				return err
			}
			chown(dest)
		case entryFile:
			if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(dest) // never write through an existing symlink
			}
			src, err := e.open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, e.Mode|0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, io.LimitReader(src, budget+1))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			budget -= n
			if budget < 0 {
				return fmt.Errorf("%w: the archive unpacks to more than %d GiB", validate.ErrInvalid, MaxExtractBytes>>30)
			}
			chown(dest)
		}
		return nil
	})
}

// mkdirAll creates dir and its missing parents below root, owned like the files.
func mkdirAll(dir, root string, chown func(string)) error {
	if dir == root || len(dir) <= len(root) {
		return nil
	}
	if _, err := os.Lstat(dir); err == nil {
		return nil
	}
	if err := mkdirAll(filepath.Dir(dir), root, chown); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	chown(dir)
	return nil
}

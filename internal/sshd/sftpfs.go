package sshd

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"

	"github.com/seramos/staqio/internal/project"
)

// projectFS exposes the container's bind mounts over SFTP, using the container's paths:
// /var/www/html (the project directory), /home/staqio (the persistent tool home) and,
// with Gateway enabled, the shared JetBrains cache below it. Everything is served from
// the Staqio side of the same bind mounts; created files are chowned to the project
// owner so the containers can use them.
type projectFS struct {
	roots map[string]string // container path prefix → Staqio-side directory
	// prefixes are the root paths, longest first, so nested mounts win.
	prefixes []string
	uid      int
	gid      int
}

func newProjectFS(t project.ExecTarget) *projectFS {
	uid, gid := 0, 0
	if parts := strings.SplitN(t.User, ":", 2); len(parts) == 2 {
		uid, _ = strconv.Atoi(parts[0])
		gid, _ = strconv.Atoi(parts[1])
	}
	roots := map[string]string{t.AppMount: t.ProjectDir, t.HomeMount: t.HomeDir}
	for k, v := range t.Mounts {
		roots[k] = v
	}
	prefixes := make([]string, 0, len(roots))
	for k := range roots {
		prefixes = append(prefixes, k)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	return &projectFS{roots: roots, prefixes: prefixes, uid: uid, gid: gid}
}

var errOutside = sftp.ErrSSHFxPermissionDenied

// resolve maps a container path to a Staqio-side path. It returns ok=false for the
// virtual directories above the roots ("/", "/var", "/var/www", "/home").
func (f *projectFS) resolve(p string) (local string, ok bool, err error) {
	clean := path.Clean("/" + p)
	for _, prefix := range f.prefixes {
		root := f.roots[prefix]
		if clean == prefix {
			return root, true, nil
		}
		if strings.HasPrefix(clean, prefix+"/") {
			rel := strings.TrimPrefix(clean, prefix+"/")
			local = filepath.Join(root, filepath.FromSlash(rel))
			// Lexical containment (symlinks are resolved by the OS inside the project tree).
			if r, err := filepath.Rel(root, local); err != nil || r == ".." || strings.HasPrefix(r, "../") {
				return "", false, errOutside
			}
			return local, true, nil
		}
	}
	return "", false, nil
}

// mountsBelow returns the names of mount points that are direct children of dir.
func (f *projectFS) mountsBelow(dir string) []string {
	var names []string
	for _, prefix := range f.prefixes {
		if path.Dir(prefix) == dir && prefix != dir {
			names = append(names, path.Base(prefix))
		}
	}
	return names
}

// virtualDirs are the directories between "/" and the roots.
var virtualDirs = map[string][]string{
	"/":        {"home", "var"},
	"/var":     {"www"},
	"/var/www": {"html"},
	"/home":    {"staqio"},
}

type virtualInfo struct {
	name string
	dir  bool
}

func (v virtualInfo) Name() string       { return v.name }
func (v virtualInfo) Size() int64        { return 0 }
func (v virtualInfo) Mode() os.FileMode  { return os.ModeDir | 0o755 }
func (v virtualInfo) ModTime() time.Time { return time.Time{} }
func (v virtualInfo) IsDir() bool        { return v.dir }
func (v virtualInfo) Sys() any           { return nil }

type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}

// Fileread implements sftp.FileReader.
func (f *projectFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	local, ok, err := f.resolve(r.Filepath)
	if err != nil || !ok {
		return nil, errOutside
	}
	return os.Open(local)
}

// Filewrite implements sftp.FileWriter.
func (f *projectFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	local, ok, err := f.resolve(r.Filepath)
	if err != nil || !ok {
		return nil, errOutside
	}
	flags := os.O_WRONLY | os.O_CREATE
	pf := r.Pflags()
	if pf.Trunc {
		flags |= os.O_TRUNC
	}
	if pf.Append {
		flags |= os.O_APPEND
	}
	if pf.Excl {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(local, flags, 0o644)
	if err != nil {
		return nil, err
	}
	_ = file.Chown(f.uid, f.gid)
	return file, nil
}

// Filecmd implements sftp.FileCmder.
func (f *projectFS) Filecmd(r *sftp.Request) error {
	local, ok, err := f.resolve(r.Filepath)
	if err != nil || !ok {
		return errOutside
	}
	switch r.Method {
	case "Mkdir":
		if err := os.Mkdir(local, 0o755); err != nil {
			return err
		}
		return os.Chown(local, f.uid, f.gid)
	case "Rmdir":
		return os.Remove(local)
	case "Remove":
		if st, err := os.Lstat(local); err == nil && st.IsDir() {
			return syscall.EISDIR
		}
		return os.Remove(local)
	case "Rename":
		target, ok, err := f.resolve(r.Target)
		if err != nil || !ok {
			return errOutside
		}
		return os.Rename(local, target)
	case "Symlink":
		// Only relative links that stay inside the same tree are allowed.
		if path.IsAbs(r.Target) || strings.Contains(r.Target, "..") {
			return errOutside
		}
		return os.Symlink(r.Target, local)
	case "Setstat":
		attrs := r.Attributes()
		flags := r.AttrFlags()
		if flags.Permissions {
			if err := os.Chmod(local, os.FileMode(attrs.Mode)&os.ModePerm); err != nil {
				return err
			}
		}
		if flags.Size {
			if err := os.Truncate(local, int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			t := time.Unix(int64(attrs.Mtime), 0)
			if err := os.Chtimes(local, time.Unix(int64(attrs.Atime), 0), t); err != nil {
				return err
			}
		}
		return nil
	}
	return sftp.ErrSSHFxOpUnsupported
}

// Filelist implements sftp.FileLister (List, Stat, Readlink).
func (f *projectFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	clean := path.Clean("/" + r.Filepath)
	local, ok, err := f.resolve(clean)
	if err != nil {
		return nil, err
	}
	switch r.Method {
	case "List":
		if !ok {
			names, virtual := virtualDirs[clean]
			if !virtual {
				return nil, os.ErrNotExist
			}
			infos := make([]os.FileInfo, 0, len(names))
			for _, n := range names {
				infos = append(infos, virtualInfo{name: n, dir: true})
			}
			return listerAt(infos), nil
		}
		entries, err := os.ReadDir(local)
		mounts := f.mountsBelow(clean)
		if err != nil && !(len(mounts) > 0 && os.IsNotExist(err)) {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries)+len(mounts))
		seen := map[string]bool{}
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				infos = append(infos, info)
				seen[info.Name()] = true
			}
		}
		// Nested mounts (e.g. /home/staqio/.cache/JetBrains) are not files of the parent
		// directory on the Staqio side; show them the way the container sees them.
		for _, m := range mounts {
			if !seen[m] {
				infos = append(infos, virtualInfo{name: m, dir: true})
			}
		}
		return listerAt(infos), nil
	case "Stat", "Lstat":
		if !ok {
			if _, virtual := virtualDirs[clean]; !virtual {
				return nil, os.ErrNotExist
			}
			return listerAt{virtualInfo{name: path.Base(clean), dir: true}}, nil
		}
		var info os.FileInfo
		if r.Method == "Lstat" {
			info, err = os.Lstat(local)
		} else {
			info, err = os.Stat(local)
		}
		if err != nil {
			return nil, err
		}
		return listerAt{info}, nil
	case "Readlink":
		if !ok {
			return nil, errOutside
		}
		target, err := os.Readlink(local)
		if err != nil {
			return nil, err
		}
		return listerAt{virtualInfo{name: target}}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// Lstat implements sftp.LstatFileLister.
func (f *projectFS) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	r.Method = "Lstat"
	return f.Filelist(r)
}

var _ sftp.LstatFileLister = (*projectFS)(nil)

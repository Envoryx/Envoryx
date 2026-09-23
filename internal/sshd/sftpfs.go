package sshd

import (
	"context"
	"errors"
	"fmt"
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

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
)

// projectFS exposes the container's bind mounts over SFTP, using the container's paths:
// /var/www/html (the project directory), /home/envoryx (the persistent tool home) and,
// with Gateway enabled, the shared JetBrains cache below it. Everything is served from
// the Envoryx side of the same bind mounts; created files are chowned to the project
// owner so the containers can use them.
type projectFS struct {
	roots map[string]string // container path prefix → Envoryx-side directory
	// prefixes are the root paths, longest first, so nested mounts win.
	prefixes []string
	uid      int
	gid      int
	// statOutside answers Stat/Lstat for paths outside the mounts from the running
	// container, read-only: IDEs check that an interpreter such as /usr/local/bin/php
	// exists over SFTP before they use it. Nil when the container is not running.
	statOutside func(p string, follow bool) (os.FileInfo, error)
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

// resolve maps a container path to a Envoryx-side path. It returns ok=false for the
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
	"/home":    {"envoryx"},
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

// clientErr drops the Envoryx-side path from filesystem errors: the client addresses
// files by their container path and has no business seeing where they live here.
func clientErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// Fileread implements sftp.FileReader.
func (f *projectFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	local, ok, err := f.resolve(r.Filepath)
	if err != nil || !ok {
		return nil, errOutside
	}
	file, err := os.Open(local)
	if err != nil {
		return nil, clientErr(err)
	}
	return file, nil
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
		return nil, clientErr(err)
	}
	_ = file.Chown(f.uid, f.gid)
	return file, nil
}

// Filecmd implements sftp.FileCmder.
func (f *projectFS) Filecmd(r *sftp.Request) error { return clientErr(f.filecmd(r)) }

func (f *projectFS) filecmd(r *sftp.Request) error {
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
	l, err := f.filelist(r)
	return l, clientErr(err)
}

func (f *projectFS) filelist(r *sftp.Request) (sftp.ListerAt, error) {
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
		// Nested mounts (e.g. /home/envoryx/.cache/JetBrains) are not files of the parent
		// directory on the Envoryx side; show them the way the container sees them.
		for _, m := range mounts {
			if !seen[m] {
				infos = append(infos, virtualInfo{name: m, dir: true})
			}
		}
		return listerAt(infos), nil
	case "Stat", "Lstat":
		if !ok {
			if _, virtual := virtualDirs[clean]; virtual {
				return listerAt{virtualInfo{name: path.Base(clean), dir: true}}, nil
			}
			if f.statOutside == nil {
				return nil, os.ErrNotExist
			}
			info, err := f.statOutside(clean, r.Method == "Stat")
			if err != nil {
				return nil, err
			}
			return listerAt{info}, nil
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

// containerStat stats a path inside the container with stat(1), which coreutils and
// busybox both provide, as the container's default user.
func containerStat(ctx context.Context, engine docker.Engine, containerID string) func(string, bool) (os.FileInfo, error) {
	return func(p string, follow bool) (os.FileInfo, error) {
		cmd := []string{"stat", "-c", "%f %s %Y", "--", p}
		if follow {
			cmd = []string{"stat", "-L", "-c", "%f %s %Y", "--", p}
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		res, err := engine.Exec(ctx, containerID, cmd, nil)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, os.ErrNotExist
		}
		return parseStat(path.Base(p), res.Stdout)
	}
}

// parseStat reads "<raw mode in hex> <size> <mtime>" as printed by stat -c '%f %s %Y'.
func parseStat(name, out string) (os.FileInfo, error) {
	fields := strings.Fields(out)
	if len(fields) != 3 {
		return nil, fmt.Errorf("unexpected stat output %q", out)
	}
	raw, err1 := strconv.ParseUint(fields[0], 16, 32)
	size, err2 := strconv.ParseInt(fields[1], 10, 64)
	mtime, err3 := strconv.ParseInt(fields[2], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, fmt.Errorf("unexpected stat output %q: %w", out, err)
	}
	mode := os.FileMode(raw & 0o777)
	switch raw & syscall.S_IFMT {
	case syscall.S_IFDIR:
		mode |= os.ModeDir
	case syscall.S_IFLNK:
		mode |= os.ModeSymlink
	case syscall.S_IFREG:
	default:
		mode |= os.ModeIrregular
	}
	return statInfo{name: name, size: size, mode: mode, mtime: time.Unix(mtime, 0)}, nil
}

type statInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func (i statInfo) Name() string       { return i.name }
func (i statInfo) Size() int64        { return i.size }
func (i statInfo) Mode() os.FileMode  { return i.mode }
func (i statInfo) ModTime() time.Time { return i.mtime }
func (i statInfo) IsDir() bool        { return i.mode.IsDir() }
func (i statInfo) Sys() any           { return nil }

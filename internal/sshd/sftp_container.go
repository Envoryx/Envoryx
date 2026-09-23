package sshd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// containerOps serves SFTP requests for paths outside the bind mounts from the running
// application container, as the project user: stat, ls, mkdir, rm, mv, cat and chmod run
// there, so the answers and the permissions are the ones a shell over the same SSH
// access gets. IDEs rely on this – PhpStorm checks /usr/local/bin/php, PyCharm uploads
// the project to /tmp/pycharm_project_* before its settings can even be changed. The
// tools are the ones coreutils and busybox both provide.
type containerOps struct {
	ctx         context.Context
	engine      docker.Engine
	containerID string
	user        string
}

// maxOutsideRead bounds a file read through the container; SFTP outside the mounts is for
// IDE helpers and small files, the project itself lives in the mounts.
const maxOutsideRead = 64 << 20

func (c *containerOps) run(stdin io.Reader, cmd ...string) (stdout, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
	defer cancel()
	var out, errOut bytes.Buffer
	code, err = c.engine.ExecStream(ctx, c.containerID, docker.ExecStreamOptions{Cmd: cmd, User: c.user, Stdin: stdin, Stdout: &out, Stderr: &errOut})
	return out.String(), errOut.String(), code, err
}

// check turns a failed command into the error an SFTP server on the container would send.
func check(stderr string, code int, err error) error {
	if err != nil {
		return err
	}
	switch {
	case code == 0:
		return nil
	case strings.Contains(stderr, "No such file"):
		return os.ErrNotExist
	case strings.Contains(stderr, "File exists"):
		return os.ErrExist
	}
	return os.ErrPermission
}

func (c *containerOps) stat(p string, follow bool) (os.FileInfo, error) {
	cmd := []string{"stat", "-c", "%f %s %Y %u %g", "--", p}
	if follow {
		cmd = []string{"stat", "-L", "-c", "%f %s %Y %u %g", "--", p}
	}
	out, _, code, err := c.run(nil, cmd...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, os.ErrNotExist
	}
	return parseStat(path.Base(p), out)
}

// list returns the entries of a directory, hidden ones included.
func (c *containerOps) list(dir string) ([]os.FileInfo, error) {
	const script = `cd -- "$1" || exit 1
for f in * .[!.]* ..?*; do
  if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\n' "$(stat -c '%f %s %Y %u %g' -- "$f")/$f"; fi
done`
	out, stderr, code, err := c.run(nil, "sh", "-c", script, "envoryx-ls", dir)
	if err := check(stderr, code, err); err != nil {
		return nil, err
	}
	var infos []os.FileInfo
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		meta, name, ok := strings.Cut(line, "/")
		if !ok || name == "" {
			continue
		}
		if info, err := parseStat(name, meta); err == nil {
			infos = append(infos, info)
		}
	}
	return infos, nil
}

func (c *containerOps) mkdir(p string) error {
	_, stderr, code, err := c.run(nil, "mkdir", "--", p)
	return check(stderr, code, err)
}

func (c *containerOps) remove(p string) error {
	_, stderr, code, err := c.run(nil, "rm", "--", p)
	return check(stderr, code, err)
}

func (c *containerOps) rmdir(p string) error {
	_, stderr, code, err := c.run(nil, "rmdir", "--", p)
	return check(stderr, code, err)
}

func (c *containerOps) rename(from, to string) error {
	_, stderr, code, err := c.run(nil, "mv", "--", from, to)
	return check(stderr, code, err)
}

func (c *containerOps) chmod(p string, mode os.FileMode) error {
	_, stderr, code, err := c.run(nil, "chmod", strconv.FormatUint(uint64(mode.Perm()), 8), "--", p)
	return check(stderr, code, err)
}

func (c *containerOps) readlink(p string) (string, error) {
	out, stderr, code, err := c.run(nil, "readlink", "--", p)
	if err := check(stderr, code, err); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

func (c *containerOps) read(p string) (io.ReaderAt, error) {
	out, stderr, code, err := c.run(nil, "sh", "-c", `head -c "$2" -- "$1"`, "envoryx-read", p, strconv.Itoa(maxOutsideRead+1))
	if err := check(stderr, code, err); err != nil {
		return nil, err
	}
	if len(out) > maxOutsideRead {
		return nil, os.ErrPermission
	}
	return strings.NewReader(out), nil
}

// write returns a writer that collects the upload in a temporary file and hands it to the
// container when the client closes the handle.
func (c *containerOps) write(p string, appendTo bool) (io.WriterAt, error) {
	tmp, err := os.CreateTemp("", "envoryx-sftp-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(tmp.Name()) // unlinked: gone with the handle
	w := &containerWriter{ops: c, path: p, appendTo: appendTo, tmp: tmp}
	// Create the file right away, as an open() on a real server would: clients stat it
	// before the first write, and a refused path must fail the open, not the close.
	if !appendTo {
		if err := w.flush(); err != nil {
			_ = tmp.Close()
			return nil, err
		}
	}
	return w, nil
}

type containerWriter struct {
	ops      *containerOps
	path     string
	appendTo bool
	tmp      *os.File
}

func (w *containerWriter) WriteAt(b []byte, off int64) (int, error) { return w.tmp.WriteAt(b, off) }

func (w *containerWriter) flush() error {
	if _, err := w.tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	redirect := `cat > "$1"`
	if w.appendTo {
		redirect = `cat >> "$1"`
	}
	_, stderr, code, err := w.ops.run(w.tmp, "sh", "-c", redirect, "envoryx-write", w.path)
	return check(stderr, code, err)
}

func (w *containerWriter) Close() error {
	defer w.tmp.Close()
	return w.flush()
}

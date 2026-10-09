package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// maxProjectFile caps what Envoryx reads of a project's own files (composer.json,
// go.mod, a .csproj …) to look at them.
const maxProjectFile = 1 << 20

var errNotRegular = errors.New("not a regular file")

// readProjectFile reads a file below a project's directory for Envoryx itself. The project
// directory belongs to whoever works in the project, so the file may be anything: a
// symbolic link to /dev/zero or out of the project, a pipe that never ends, a gigabyte. It
// is opened through an os.Root of dir (links are followed only while they stay inside),
// without blocking, and read only when it is a regular file of at most max bytes.
func readProjectFile(dir, rel string, max int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, info, err := openProjectFile(root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", rel, max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", rel, max)
	}
	return b, nil
}

// openProjectFile opens a regular file below root for reading, without blocking on a
// pipe, and refuses anything else. It's what the walks over a project tree (backups,
// copies) use to read the files they found: between the walk and the open the entry may
// have become a link or a pipe.
func openProjectFile(root *os.Root, rel string) (*os.File, os.FileInfo, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w", rel, errNotRegular)
	}
	return f, info, nil
}

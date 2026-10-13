// Package archivetest builds archives from a compact text description and checks that
// unpacking one stayed inside its target, for the fuzz tests of the archive extractors.
package archivetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Member is one archive entry of a Spec.
type Member struct {
	// Kind is 'd' (directory), 'l' (symlink), 'h' (hard link) or anything else for a
	// regular file.
	Kind byte
	Name string
	// Body is a file's contents or a link's target.
	Body string
}

// Parse reads a description: one member per line, "<kind>|<name>|<body>". It lets the
// fuzzer mutate names, links and their order rather than tar header bytes, which would
// almost never give a readable archive.
func Parse(spec string) []Member {
	var out []Member
	for line := range strings.SplitSeq(spec, "\n") {
		kind, rest, ok := strings.Cut(line, "|")
		if !ok || kind == "" {
			continue
		}
		name, body, _ := strings.Cut(rest, "|")
		out = append(out, Member{Kind: kind[0], Name: name, Body: body})
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// Tar writes the members as a tar stream, gzipped if gz is set. Members tar refuses
// (an over-long name, say) are left out.
func Tar(members []Member, gz bool) []byte {
	var buf bytes.Buffer
	var zw *gzip.Writer
	tw := tar.NewWriter(&buf)
	if gz {
		zw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(zw)
	}
	for _, m := range members {
		hdr := &tar.Header{Name: m.Name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(m.Body)), Format: tar.FormatPAX}
		switch m.Kind {
		case 'd':
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		case 'l':
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, m.Body, 0
		case 'h':
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeLink, m.Body, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			continue
		}
		if hdr.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(m.Body))
		}
	}
	_ = tw.Close()
	if zw != nil {
		_ = zw.Close()
	}
	return buf.Bytes()
}

// Zip writes the members as a ZIP archive; symlinks get the Unix mode bits that mark
// them, as Info-ZIP writes them.
func Zip(members []Member) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.Name, Method: zip.Store}
		switch m.Kind {
		case 'd':
			if !strings.HasSuffix(h.Name, "/") {
				h.Name += "/"
			}
			h.SetMode(fs.ModeDir | 0o755)
		case 'l':
			h.SetMode(fs.ModeSymlink | 0o777)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			continue
		}
		if m.Kind != 'd' {
			_, _ = w.Write([]byte(m.Body))
		}
	}
	_ = zw.Close()
	return buf.Bytes()
}

// Sandbox is a scratch directory holding the extraction target and, next to it, a
// directory an escaping archive could reach.
type Sandbox struct {
	Base, Target, Outside string
}

// NewSandbox prepares the directories; the target holds a symlink "up" to the outside
// directory, as a project's owner could have put there, which nothing may be written
// through.
func NewSandbox(t *testing.T) Sandbox {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Sandbox{Base: base, Target: filepath.Join(base, "target"), Outside: filepath.Join(base, "outside")}
	for _, d := range []string{s.Target, s.Outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.Outside, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(s.Target, "up")); err != nil {
		t.Fatal(err)
	}
	return s
}

// Check fails the test if the extraction touched anything outside the target or left a
// symlink in it that leads out of it.
func (s Sandbox) Check(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(s.Base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "target" && e.Name() != "outside" {
			t.Fatalf("%s was created next to the target", e.Name())
		}
	}
	entries, err = os.ReadDir(s.Outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("the outside directory was changed: %v", entries)
	}
	if b, err := os.ReadFile(filepath.Join(s.Outside, "keep.txt")); err != nil || string(b) != "keep" {
		t.Fatalf("keep.txt was changed: %q %v", b, err)
	}
	// Links through the owner's own "up" lead out by the owner's doing; without it they
	// end nowhere.
	if fi, err := os.Lstat(filepath.Join(s.Target, "up")); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		_ = os.Remove(filepath.Join(s.Target, "up"))
	}
	root, err := os.OpenRoot(s.Target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	err = filepath.WalkDir(s.Target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Make directories readable again for the walk and for t.TempDir's clean-up.
		if d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, _ := filepath.Rel(s.Target, p)
		link, _ := os.Readlink(p)
		if filepath.IsAbs(link) {
			if r, err := filepath.Rel(s.Target, filepath.Clean(link)); err != nil || r == ".." || strings.HasPrefix(r, "../") {
				t.Errorf("symlink %s -> %s leads out of the target", rel, link)
			}
			return nil
		}
		// os.Root resolves the link the way the kernel does and refuses a path that
		// leaves it, even when its end does not exist.
		if _, err := root.Stat(rel); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) && strings.Contains(err.Error(), "escapes") {
			t.Errorf("symlink %s -> %s leads out of the target", rel, link)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

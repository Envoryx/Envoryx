package project

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A project's files belong to whoever works in the project: Envoryx reads them without
// hanging on a pipe, without filling its memory from /dev/zero and without leaving the
// project through a link.
func TestReadProjectFileRefusesWhatIsNoPlainFile(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink("composer.json", filepath.Join(dir, "inside.json"))
	_ = os.Symlink("/dev/zero", filepath.Join(dir, "zero.json"))
	_ = os.Symlink(outside, filepath.Join(dir, "out.json"))
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.json"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	if b, err := readProjectFile(dir, "composer.json", 1024); err != nil || string(b) != `{"require":{}}` {
		t.Fatalf("plain file: %q %v", b, err)
	}
	if _, err := readProjectFile(dir, "inside.json", 1024); err != nil {
		t.Fatalf("a link inside the project: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, bad := range []string{"zero.json", "out.json", "fifo.json", "big.json", "missing.json"} {
			if b, err := readProjectFile(dir, bad, 1024); err == nil {
				t.Errorf("%s read: %d bytes", bad, len(b))
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading the project's files hangs")
	}
}

// A link the project's owner put into the project home must not make Envoryx (root)
// create, chown or write anything where it points.
func TestWritePlanFilesFollowsNoLinks(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "projects", "p1", "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		BaseDir: base,
		Dirs:    []DirPlan{{Path: filepath.Join(home, ".config", "pnpm")}},
		Files:   []FilePlan{{Path: filepath.Join(home, ".config", "pnpm", "rc"), Content: "store-dir=/x\n", Seed: true}},
	}
	err := writePlanFiles(plan)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a link on the way: %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("written through the link: %v", entries)
	}
	// Without the link the same plan goes through.
	_ = os.Remove(filepath.Join(home, ".config"))
	if err := writePlanFiles(plan); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".config", "pnpm", "rc")); err != nil || string(b) != "store-dir=/x\n" {
		t.Fatalf("seed file: %q %v", b, err)
	}
	// A planned file that is a link is not written through either.
	conf := filepath.Join(base, "projects", "p1", "php")
	_ = os.MkdirAll(conf, 0o755)
	_ = os.Symlink(filepath.Join(elsewhere, "victim"), filepath.Join(conf, "zz.ini"))
	err = writePlanFiles(Plan{BaseDir: base, Files: []FilePlan{{Path: filepath.Join(conf, "zz.ini"), Content: "x"}}})
	if err == nil {
		t.Fatal("a file written through a link")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "victim")); err == nil {
		t.Fatal("the link's target was created")
	}
}

// A restore into a project that holds a link to a directory elsewhere (d -> /config)
// must not change that directory's mode or put files into it.
func TestExtractArchiveStaysOutOfPlantedLinks(t *testing.T) {
	target := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Chmod(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(target, "d")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	f, _ := os.Create(archive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o777})
	_ = tw.WriteHeader(&tar.Header{Name: "d/x.php", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	_ = extractArchive(archive, target, os.Getuid(), os.Getgid())
	if info, err := os.Stat(elsewhere); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode of the directory elsewhere: %v %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("restored into the directory elsewhere: %v", entries)
	}
}

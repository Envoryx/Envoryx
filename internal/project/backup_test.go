package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestBackupAndRestore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var dumpEnv []string
	var restored []byte
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			dumpEnv = env
			return "-- MariaDB dump\nCREATE TABLE t (id int);\n", 0, nil
		case "mariadb":
			restored = stdin
			return "", 0, nil
		}
		return "", 1, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	projDir := filepath.Join(e.projDir, "shop")
	_ = os.MkdirAll(filepath.Join(projDir, "vendor", "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(projDir, "vendor", "lib", "x.php"), []byte("dep"), 0o644)
	_ = os.WriteFile(filepath.Join(projDir, "public", "index.php"), []byte("v1"), 0o644)
	_ = os.Symlink("public", filepath.Join(projDir, "www"))

	// Backup without dependencies.
	info, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Files: true, Note: "before deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "full" || info.Meta.Database == nil || info.Meta.Files == nil || info.SizeBytes == 0 || info.Meta.Note != "before deploy" {
		t.Fatalf("backup info: %+v", info)
	}
	creds, _ := e.m.DatabaseCredentials(ctx, id)
	if !strings.Contains(strings.Join(dumpEnv, ","), "MYSQL_PWD="+creds.RootPassword) {
		t.Fatal("dump must receive the root password via env")
	}
	dir := filepath.Join(e.cfgDir, "backups", "shop", info.Dir)
	for _, f := range []string{"backup.json", "database.sql.gz", "files.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s missing: %v", f, err)
		}
	}
	names := tarNames(t, filepath.Join(dir, "files.tar.gz"))
	if !contains(names, "public/index.php") || !contains(names, "www") || contains(names, "vendor/lib/x.php") {
		t.Fatalf("archive entries: %v", names)
	}
	meta, _ := os.ReadFile(filepath.Join(dir, "backup.json"))
	if !bytes.Contains(meta, []byte(creds.Password)) || !bytes.Contains(meta, []byte(`"slug": "shop"`)) {
		t.Fatal("backup.json must contain the project export with credentials")
	}

	// With dependencies.
	info2, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true, IncludeDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	if info2.Kind != "files" || info2.Meta.Database != nil {
		t.Fatalf("files-only backup: %+v", info2)
	}
	if names := tarNames(t, filepath.Join(e.cfgDir, "backups", "shop", info2.Dir, "files.tar.gz")); !contains(names, "vendor/lib/x.php") {
		t.Fatalf("dependencies must be included: %v", names)
	}

	list, err := e.m.ListBackups(ctx, id)
	if err != nil || len(list) != 2 || list[0].ID != info2.ID {
		t.Fatalf("list: %+v %v", list, err)
	}

	// Modify, then restore.
	_ = os.WriteFile(filepath.Join(projDir, "public", "index.php"), []byte("v2"), 0o644)
	_ = os.WriteFile(filepath.Join(projDir, "new.txt"), []byte("new"), 0o644)
	if _, err := e.m.RestoreBackup(ctx, id, info.ID, RestoreOptions{Database: true, Files: true, Confirm: "wrong"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("restore needs confirmation, got %v", err)
	}
	if _, err := e.m.RestoreBackup(ctx, id, info2.ID, RestoreOptions{Database: true, Confirm: "shop"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("restoring a database from a files-only backup must fail, got %v", err)
	}
	if _, err := e.m.RestoreBackup(ctx, id, info.ID, RestoreOptions{Database: true, Files: true, WipeFiles: true, Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if string(restored) != "-- MariaDB dump\nCREATE TABLE t (id int);\n" {
		t.Fatalf("restored dump: %q", restored)
	}
	if b, _ := os.ReadFile(filepath.Join(projDir, "public", "index.php")); string(b) != "v1" {
		t.Fatalf("file not restored: %q", b)
	}
	if _, err := os.Stat(filepath.Join(projDir, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wipe must remove files not in the backup")
	}
	if link, err := os.Readlink(filepath.Join(projDir, "www")); err != nil || link != "public" {
		t.Fatalf("symlink not restored: %q %v", link, err)
	}

	// Download archive and delete.
	rc, name, err := e.m.OpenBackupArchive(ctx, id, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.HasSuffix(name, ".tar") || len(data) == 0 {
		t.Fatalf("download: %s %d", name, len(data))
	}
	if err := e.m.DeleteBackup(ctx, id, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup dir must be removed")
	}
	if list, _ := e.m.ListBackups(ctx, id); len(list) != 1 {
		t.Fatalf("list after delete: %+v", list)
	}
	entries, _ := e.store.Audit.Recent(ctx, 20)
	for _, en := range entries {
		if strings.Contains(string(en.Details), creds.Password) {
			t.Fatal("audit must not contain secrets")
		}
	}
}

func TestExtractArchiveRejectsTarSlip(t *testing.T) {
	target := t.TempDir()
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(archive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4})
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	if err := extractArchive(archive, target, os.Getuid(), os.Getgid()); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("path traversal must be rejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "escape.txt")); err == nil {
		t.Fatal("file escaped the target directory")
	}

	// Symlink to outside, then a file through it.
	archive2 := filepath.Join(t.TempDir(), "link.tar.gz")
	f2, _ := os.Create(archive2)
	gz2 := gzip.NewWriter(f2)
	tw2 := tar.NewWriter(gz2)
	_ = tw2.WriteHeader(&tar.Header{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "/tmp", Mode: 0o777})
	_ = tw2.WriteHeader(&tar.Header{Name: "out/pwned.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw2.Write([]byte("x"))
	_ = tw2.Close()
	_ = gz2.Close()
	_ = f2.Close()
	target2 := t.TempDir()
	err := extractArchive(archive2, target2, os.Getuid(), os.Getgid())
	if _, statErr := os.Lstat(filepath.Join(target2, "out")); statErr == nil {
		if fi, _ := os.Lstat(filepath.Join(target2, "out")); fi.Mode()&os.ModeSymlink != 0 {
			t.Fatal("symlink pointing outside must not be created")
		}
	}
	if _, statErr := os.Stat("/tmp/pwned.txt"); statErr == nil {
		os.Remove("/tmp/pwned.txt")
		t.Fatal("file written through symlink outside the target")
	}
	_ = err
}

func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, strings.TrimSuffix(h.Name, "/"))
	}
	return names
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

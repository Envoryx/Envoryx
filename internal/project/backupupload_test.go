package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

// A downloaded backup uploaded again (here, after its project and its backups were
// deleted) becomes a backup of the deleted project, restorable into a new project.
func TestImportBackupFile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v1"), 0o644)
	b, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := e.m.OpenBackupArchive(ctx, id, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	dir := t.TempDir()
	plain := filepath.Join(dir, "shop.tar")
	_ = os.WriteFile(plain, raw, 0o600)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	packed := filepath.Join(dir, "shop.tar.gz")
	_ = os.WriteFile(packed, gz.Bytes(), 0o600)
	site := filepath.Join(dir, "site.tar")
	_ = os.WriteFile(site, []byte("not a tar"), 0o600)
	if !IsBackupArchive(plain) || !IsBackupArchive(packed) || IsBackupArchive(site) {
		t.Fatal("backup archives not told apart")
	}

	// Its project is still there: the upload is that project's backup.
	up, err := e.m.ImportBackupFile(ctx, plain)
	if err != nil || !up.ProjectExists || up.Backup.ID != b.ID || up.ProjectName != "Shop" {
		t.Fatalf("existing project: %+v %v", up, err)
	}

	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "shop", DeleteFiles: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.DeleteOrphanedBackup(ctx, id, b.ID); err != nil {
		t.Fatal(err)
	}
	up, err = e.m.ImportBackupFile(ctx, packed)
	if err != nil {
		t.Fatal(err)
	}
	if up.ProjectExists || up.ProjectID != id || up.Slug != "shop" || up.Backup.Dir != b.Dir || up.Backup.Meta.Files == nil {
		t.Fatalf("deleted project: %+v", up)
	}
	if orphans, _ := e.m.OrphanedBackups(ctx); len(orphans) != 1 || orphans[0].Backup.ID != up.Backup.ID {
		t.Fatalf("orphans: %+v", orphans)
	}
	// Uploaded twice, it stays one backup.
	if again, err := e.m.ImportBackupFile(ctx, plain); err != nil || again.Backup.ID != up.Backup.ID {
		t.Fatalf("second upload: %+v %v", again, err)
	}
	back, err := e.m.RestoreIntoNewProject(ctx, id, up.Backup.ID, RestoreNewRequest{Name: "Shop", Files: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.projDir, "shop", "public", "index.php")); back.Project.Slug != "shop" || string(got) != "v1" {
		t.Fatalf("restored: %+v %q", back.Project, got)
	}

	if _, err := e.m.ImportBackupFile(ctx, site); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("not a backup: %v", err)
	}
}

// A backup whose project settings can't be read here is refused with the reason, not as
// an internal error.
func TestImportBackupFileRefusesUnreadableSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	archive := func(sealed string) string {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		meta, _ := json.Marshal(map[string]any{"format": 1, "projectId": "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f", "projectName": "Gone", "slug": "gone", "sealedProject": sealed})
		_ = tw.WriteHeader(&tar.Header{Name: "gone-20261010-010203-abcdef01/backup.json", Mode: 0o600, Size: int64(len(meta)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(meta)
		_ = tw.Close()
		f := filepath.Join(t.TempDir(), "gone.tar")
		_ = os.WriteFile(f, buf.Bytes(), 0o600)
		return f
	}
	for sealed, want := range map[string]string{
		"envoryx:v1:deadbeef:AAAA": "secret key of another Envoryx",
		"{not json":                "unreadable",
	} {
		_, err := e.m.ImportBackupFile(ctx, archive(sealed))
		if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", sealed, err)
		}
	}
}

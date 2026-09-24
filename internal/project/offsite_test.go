package project

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestOffsiteArchiveRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	var hooked []BackupInfo
	e.m.SetBackupHook(func(_ string, b BackupInfo) { hooked = append(hooked, b) })
	b, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true, Note: "before", Source: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hooked) != 1 || hooked[0].ID != b.ID || hooked[0].Meta.Source != "scheduled" {
		t.Fatalf("hook: %+v", hooked)
	}
	a, err := e.m.OpenOffsiteArchive(ctx, id, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(a.Body)
	a.Body.Close()
	if a.Slug != "shop" || a.Dir != b.Dir || a.Kind != "files" || a.Source != "scheduled" {
		t.Fatalf("archive: %+v", a)
	}

	// Gone locally, back from the copy: same directory, same content, restorable.
	if err := e.m.DeleteBackup(ctx, id, b.ID); err != nil {
		t.Fatal(err)
	}
	back, err := e.m.ImportBackupArchive(ctx, id, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if back.Dir != b.Dir || back.Kind != "files" || back.Meta.Note != "before" {
		t.Fatalf("imported: %+v", back)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "backups", "shop", b.Dir, backupFilesFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RestoreBackup(ctx, id, back.ID, RestoreOptions{Files: true, Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	// Importing again returns the backup that is there.
	again, err := e.m.ImportBackupArchive(ctx, id, bytes.NewReader(raw))
	if err != nil || again.ID != back.ID {
		t.Fatalf("second import: %+v %v", again, err)
	}

	// Another project does not take it; a tar with foreign entries is refused.
	other, err := e.m.Create(ctx, phpRequest("Blog", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.ImportBackupArchive(ctx, other.Project.ID, bytes.NewReader(raw)); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("foreign project: %v", err)
	}
	if _, err := e.m.ImportBackupArchive(ctx, id, bytes.NewReader([]byte("not a tar at all, just text padding padding padding"))); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestStartRecreatesAMissingProjectDirectory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Gone", false))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.projDir, v.Project.Path)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Start(ctx, v.Project.ID); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "public")); err != nil || !st.IsDir() {
		t.Fatalf("the project directory must exist before the containers mount it: %v", err)
	}
}

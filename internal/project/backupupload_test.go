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

	"github.com/envoryx/envoryx/internal/secrets"
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

	// Unpacked and packed again by hand (tar cf x.tar ./<folder>): the folder's own entry
	// comes first and the files in directory order. It is still the same backup.
	repacked := filepath.Join(dir, "repacked.tar")
	_ = os.WriteFile(repacked, repack(t, raw), 0o600)
	if !IsBackupArchive(repacked) {
		t.Fatal("a repacked backup is taken for a website")
	}
	if again, err := e.m.ImportBackupFile(ctx, repacked); err != nil || again.Backup.ID != up.Backup.ID {
		t.Fatalf("repacked upload: %+v %v", again, err)
	}
}

// repack writes the members of a backup tar again the way tar cf ./<folder> does: the
// folder entry first, then the files in reverse order, all under "./".
func repack(t *testing.T, raw []byte) []byte {
	t.Helper()
	type member struct {
		hdr  *tar.Header
		body []byte
	}
	var members []member
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		members = append(members, member{hdr, body})
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	folder := strings.SplitN(members[0].hdr.Name, "/", 2)[0]
	_ = tw.WriteHeader(&tar.Header{Name: "./" + folder + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for i := len(members) - 1; i >= 0; i-- {
		m := members[i]
		m.hdr.Name = "./" + m.hdr.Name
		_ = tw.WriteHeader(m.hdr)
		_, _ = tw.Write(m.body)
	}
	_ = tw.Close()
	return out.Bytes()
}

// A backup whose project settings can't be read here is refused with the reason, not as
// an internal error. One sealed with another Envoryx's key is kept: the restore asks for
// that key.
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
	if _, err := e.m.ImportBackupFile(ctx, archive("{not json")); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("unreadable: %v", err)
	}
	up, err := e.m.ImportBackupFile(ctx, archive("envoryx:v1:deadbeef:AAAA"))
	if err != nil || up.Backup.SealedWith != "deadbeef" {
		t.Fatalf("another key: %+v %v", up, err)
	}
	_, err = e.m.RestoreIntoNewProject(ctx, up.ProjectID, up.Backup.ID, RestoreNewRequest{Name: "Gone"})
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "sealed with secret key deadbeef, which this Envoryx does not hold") {
		t.Fatalf("restore without the key: %v", err)
	}
}

// A backup made by another Envoryx comes over with that Envoryx's key: the restore opens
// its settings with it and seals them with this Envoryx's key, which keeps the backup
// usable without it.
func TestRestoreBackupSealedWithAnotherKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	defer secrets.SetDefault(nil)
	source, _ := secrets.GenerateKey()
	secrets.SetDefault(secrets.NewRing(source))
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
	file := filepath.Join(t.TempDir(), "shop.tar")
	_ = os.WriteFile(file, raw, 0o600)
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "shop", DeleteFiles: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.DeleteOrphanedBackup(ctx, id, b.ID); err != nil {
		t.Fatal(err)
	}

	// Another Envoryx, with a key of its own.
	own, _ := secrets.GenerateKey()
	secrets.SetDefault(secrets.NewRing(own))
	up, err := e.m.ImportBackupFile(ctx, file)
	if err != nil || up.Backup.SealedWith != source.ID() {
		t.Fatalf("import: %+v %v", up, err)
	}
	if orphans, _ := e.m.OrphanedBackups(ctx); len(orphans) != 1 || orphans[0].Backup.SealedWith != source.ID() {
		t.Fatalf("orphans: %+v", orphans)
	}
	// Resealing leaves it alone instead of failing on it.
	if n, err := e.m.ResealBackups(ctx); err != nil || n != 0 {
		t.Fatalf("reseal: %d %v", n, err)
	}
	if _, err := e.m.RestoreIntoNewProject(ctx, id, up.Backup.ID, RestoreNewRequest{Name: "Shop", Files: true, Key: own.Encode()}); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "the backup needs key "+source.ID()) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := e.m.RestoreIntoNewProject(ctx, id, up.Backup.ID, RestoreNewRequest{Name: "Shop", Files: true, Key: "nope"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("no key at all: %v", err)
	}
	back, err := e.m.RestoreIntoNewProject(ctx, id, up.Backup.ID, RestoreNewRequest{Name: "Shop", Files: true, Key: source.Encode()})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.projDir, "shop", "public", "index.php")); back.Project.Slug != "shop" || string(got) != "v1" {
		t.Fatalf("restored: %+v %q", back.Project, got)
	}
	// The backup is sealed with this Envoryx's key now and restores without the other.
	orphans, _ := e.m.OrphanedBackups(ctx)
	if len(orphans) != 1 || orphans[0].Backup.SealedWith != "" {
		t.Fatalf("after the restore: %+v", orphans)
	}
	if again, err := e.m.ImportBackupFile(ctx, file); err != nil || again.Backup.ID != up.Backup.ID || again.Backup.SealedWith != "" {
		t.Fatalf("uploaded again: %+v %v", again, err)
	}
	if _, err := e.m.RestoreIntoNewProject(ctx, id, up.Backup.ID, RestoreNewRequest{Name: "Shop Again"}); err != nil {
		t.Fatal(err)
	}
}

// A backup whose settings don't open with the key that sealed them (damaged on disk)
// doesn't stop resealing, which runs at every start: it is left as it is.
func TestResealBackupsSkipsADamagedBackup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	defer secrets.SetDefault(nil)
	old, _ := secrets.GenerateKey()
	secrets.SetDefault(secrets.NewRing(old))
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	good, err := e.m.CreateBackup(ctx, v.Project.ID, BackupOptions{Files: true})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := e.m.CreateBackup(ctx, v.Project.ID, BackupOptions{Files: true})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := e.m.BackupLocation(ctx, v.Project.ID)
	file := filepath.Join(loc, bad.Dir, backupMetaFile)
	raw, _ := os.ReadFile(file)
	var bf backupFile
	_ = json.Unmarshal(raw, &bf)
	bf.SealedExport = bf.SealedExport[:len(bf.SealedExport)-4] + "AAAA"
	if err := writeBackupFile(file, bf); err != nil {
		t.Fatal(err)
	}
	damaged, _ := os.ReadFile(file)

	next, _ := secrets.GenerateKey()
	secrets.SetDefault(secrets.NewRing(next, old))
	n, err := e.m.ResealBackups(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reseal: %d %v", n, err)
	}
	if now, _ := os.ReadFile(file); string(now) != string(damaged) {
		t.Fatal("the damaged backup was changed")
	}
	raw, _ = os.ReadFile(filepath.Join(loc, good.Dir, backupMetaFile))
	_ = json.Unmarshal(raw, &bf)
	if secrets.KeyIDOf(bf.SealedExport) != next.ID() {
		t.Fatalf("the good backup is still sealed with %s", secrets.KeyIDOf(bf.SealedExport))
	}
}

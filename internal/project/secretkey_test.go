package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestSecretKeyRotation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	defer secrets.SetDefault(nil)
	ring, src, err := secrets.Load(e.cfgDir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Persist(); err != nil {
		t.Fatal(err)
	}
	secrets.SetDefault(ring)
	e.m.SetSecretKeySource(src)
	first := ring.Current().ID()

	req := phpRequest("Rot", false)
	req.Env = append(req.Env, EnvVarRequest{Key: "STRIPE_KEY", Value: "sk_live", IsSecret: true})
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	notify := filepath.Join(e.cfgDir, "notify.json")
	if err := os.WriteFile(notify, []byte(`{"token":"bot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := e.m.CreateBackup(ctx, view.Project.ID, BackupOptions{Files: true})
	if err != nil {
		t.Fatal(err)
	}
	bj := func() string {
		raw, err := os.ReadFile(filepath.Join(e.cfgDir, "backups", "rot", b.Dir, backupMetaFile))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if s := bj(); strings.Contains(s, "sk_live") || !strings.Contains(s, `"sealedProject": "envoryx:v1:`+first) {
		t.Fatalf("backup.json not sealed: %s", s)
	}
	rep, err := e.m.ResealAll(ctx)
	if err != nil || rep.Files != 1 || rep.Backups != 0 {
		t.Fatalf("reseal: %+v %v", rep, err)
	}

	info, rep, err := e.m.RotateSecretKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second := info.KeyID
	if second == first || rep.Files != 1 || rep.Backups != 1 || rep.Database == 0 {
		t.Fatalf("rotate: %+v %+v", info, rep)
	}
	keyFile, _ := os.ReadFile(filepath.Join(e.cfgDir, secrets.KeyFile))
	if k, err := secrets.ParseKey(string(keyFile)); err != nil || k.ID() != second {
		t.Fatalf("key file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, secrets.NextKeyFile)); !os.IsNotExist(err) {
		t.Fatal("next key left")
	}
	if !strings.Contains(bj(), `"sealedProject": "envoryx:v1:`+second) {
		t.Fatal("backup.json not resealed")
	}
	raw, _ := os.ReadFile(notify)
	if secrets.KeyIDOf(string(raw)) != second {
		t.Fatal("notify.json not resealed")
	}
	// Only the new key is needed from now on.
	ring.DropOld()
	got, err := e.m.Get(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got.Project.Env {
		if v.Key == "STRIPE_KEY" && v.Value != "sk_live" {
			t.Fatalf("secret after rotation: %q", v.Value)
		}
	}
	if key, err := e.m.RevealSecretKey(ctx); err != nil || strings.TrimSpace(key) != strings.TrimSpace(string(keyFile)) {
		t.Fatalf("reveal: %v", err)
	}

	// A key from the environment is changed there, not rotated here.
	e.m.SetSecretKeySource(secrets.Source{Kind: "env"})
	if _, _, err := e.m.RotateSecretKey(ctx); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("env rotation: %v", err)
	}
}

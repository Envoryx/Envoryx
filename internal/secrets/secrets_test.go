package secrets

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustKey(t *testing.T) *Key {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	k1, k2 := mustKey(t), mustKey(t)
	r := NewRing(k1)
	sealed, err := r.Seal("hunter2")
	if err != nil || !IsSealed(sealed) || strings.Contains(sealed, "hunter2") || KeyIDOf(sealed) != k1.ID() {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if again, _ := r.Seal("hunter2"); again == sealed {
		t.Fatal("the nonce must differ")
	}
	if v, err := r.Open(sealed); err != nil || v != "hunter2" {
		t.Fatalf("open: %q %v", v, err)
	}
	if v, err := r.Open("plain"); err != nil || v != "plain" {
		t.Fatalf("plain text passes: %q %v", v, err)
	}
	if v, _ := r.Seal(""); v != "" {
		t.Fatal("empty stays empty")
	}
	// Another key cannot open it; a damaged value is reported.
	if _, err := NewRing(k2).Open(sealed); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := r.Open(sealed[:len(sealed)-4] + "AAAA"); err == nil {
		t.Fatal("tampered value opened")
	}

	// Rotation: the old key still opens, reseal moves values to the new one.
	r.Rotate(k2)
	if !r.NeedsReseal(sealed) || r.NeedsReseal("") {
		t.Fatal("needs reseal")
	}
	re, changed, err := r.Reseal(sealed)
	if err != nil || !changed || KeyIDOf(re) != k2.ID() {
		t.Fatalf("reseal: %q %v %v", re, changed, err)
	}
	if _, changed, _ := r.Reseal(re); changed {
		t.Fatal("a current value is left alone")
	}
	r.DropOld()
	if _, err := r.Open(sealed); !errors.Is(err, ErrUnknownKey) {
		t.Fatal("dropped key still opens")
	}
}

func TestParseKey(t *testing.T) {
	k := mustKey(t)
	for _, s := range []string{k.Encode(), " " + k.Encode() + "\n", hex.EncodeToString(k.raw), strings.TrimRight(k.Encode(), "=")} {
		p, err := ParseKey(s)
		if err != nil || p.ID() != k.ID() {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "short", strings.Repeat("a", 40), strings.Repeat("!", 44)} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// Without anything a key file is created (0600) and used from then on.
	r1, src, err := Load(dir, getenv)
	if err != nil || src.Kind != "file" || !src.Created {
		t.Fatalf("create: %+v %v", src, err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); !os.IsNotExist(err) {
		t.Fatal("the key file is written only by Persist")
	}
	if err := src.Persist(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, KeyFile)); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	r2, src, _ := Load(dir, getenv)
	if src.Created || r2.Current().ID() != r1.Current().ID() {
		t.Fatal("the key file is reused")
	}
	old := r1.Current()

	// The environment variable wins; the file's key still opens and goes at cleanup.
	k := mustKey(t)
	env[EnvKey] = k.Encode()
	r3, src, err := Load(dir, getenv)
	if err != nil || src.Kind != "env" || r3.Current().ID() != k.ID() || !r3.Has(old.ID()) {
		t.Fatalf("env: %+v %v", src, err)
	}
	if removed := src.Cleanup(); len(removed) != 1 || removed[0] != KeyFile {
		t.Fatalf("cleanup: %v", removed)
	}
	env[EnvKey] = "nonsense"
	if _, _, err := Load(dir, getenv); err == nil || !strings.Contains(err.Error(), EnvKey) {
		t.Fatalf("bad env key: %v", err)
	}

	// A rotation that stopped after moving the old key aside: the next key takes over.
	delete(env, EnvKey)
	dir2 := t.TempDir()
	next := mustKey(t)
	if err := WriteKeyFile(filepath.Join(dir2, NextKeyFile), next); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyFile(filepath.Join(dir2, OldKeyFile), old); err != nil {
		t.Fatal(err)
	}
	r4, src, err := Load(dir2, getenv)
	if err != nil || r4.Current().ID() != next.ID() || !r4.Has(old.ID()) {
		t.Fatalf("resume rotation: %v", err)
	}
	src.Cleanup()
	if _, err := os.Stat(filepath.Join(dir2, OldKeyFile)); !os.IsNotExist(err) {
		t.Fatal("old key file left")
	}
	if _, err := os.Stat(filepath.Join(dir2, KeyFile)); err != nil {
		t.Fatal("key file missing")
	}
}

func TestFiles(t *testing.T) {
	defer SetDefault(nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "notify.json")
	if err := os.WriteFile(path, []byte(`{"token":"t0p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without a ring nothing changes.
	if changed, err := ResealFile(path); changed || err != nil {
		t.Fatal("no ring, no reseal")
	}
	k1, k2 := mustKey(t), mustKey(t)
	r := NewRing(k1)
	SetDefault(r)
	if changed, err := ResealFile(path); !changed || err != nil {
		t.Fatalf("reseal plain: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "t0p") || KeyIDOf(string(raw)) != k1.ID() {
		t.Fatalf("file not sealed: %s", raw)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal("mode lost")
	}
	if got, err := ReadFile(path); err != nil || string(got) != `{"token":"t0p"}` {
		t.Fatalf("read: %s %v", got, err)
	}
	r.Rotate(k2)
	if changed, _ := ResealFile(path); !changed {
		t.Fatal("old key not resealed")
	}
	raw, _ = os.ReadFile(path)
	if KeyIDOf(string(raw)) != k2.ID() {
		t.Fatal("not the new key")
	}
	if changed, _ := ResealFile(filepath.Join(dir, "missing.json")); changed {
		t.Fatal("missing file")
	}
	if err := WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadFile(path); string(got) != "x" {
		t.Fatal("write/read")
	}
}

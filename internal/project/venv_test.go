package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestVenvPythonVersion(t *testing.T) {
	dir := t.TempDir()
	if _, ok := venvPythonVersion(dir); ok {
		t.Fatal("no venv, no version")
	}
	venv := filepath.Join(dir, runtime.PythonVenv)
	if err := os.MkdirAll(venv, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "home = /usr/local/bin\ninclude-system-site-packages = false\nversion = 3.13.15\nexecutable = /usr/local/bin/python3.13\n"
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := venvPythonVersion(dir)
	if !ok || got != "3.13" {
		t.Fatalf("version = %q, %v", got, ok)
	}
	// A file without a version line says nothing rather than guessing.
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr/local/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := venvPythonVersion(dir); ok {
		t.Fatal("version line missing, want no answer")
	}
}

func TestRebuildHintFollowsTheProjectFiles(t *testing.T) {
	dir := t.TempDir()
	if got := rebuildHint(dir); !strings.Contains(got, "terminal") {
		t.Fatalf("nothing to install from: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rebuildHint(dir); !strings.Contains(got, "pip install -r requirements.txt") {
		t.Fatalf("requirements.txt: %q", got)
	}
	// uv wins where the project is managed by it.
	if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rebuildHint(dir); !strings.Contains(got, "uv sync") {
		t.Fatalf("uv.lock: %q", got)
	}
}

// A Python version change leaves the virtual environment behind: it is still in the
// project directory but was built for the old minor, so its packages are invisible to the
// new interpreter. The project says so until it is rebuilt.
func TestVenvWarningAfterPythonVersionChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, pythonRequest("Venv", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	dir := filepath.Join(e.projDir, v.Project.Path)
	venv := filepath.Join(dir, runtime.PythonVenv)
	if err := os.MkdirAll(venv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("version = 3.13.15\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Matching version: nothing to report.
	got, err := e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range got.Status.Warnings {
		if strings.Contains(w, "virtual environment") {
			t.Fatalf("unexpected warning while the versions match: %q", w)
		}
	}
	// Move the project to another minor.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.12", Config: runtime.PythonConfig{Server: true, Preset: "asgi", App: "main:app"}}}); err != nil {
		t.Fatal(err)
	}
	got, err = e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var warning string
	for _, w := range got.Status.Warnings {
		if strings.Contains(w, "virtual environment") {
			warning = w
		}
	}
	if !strings.Contains(warning, "built for Python 3.13") || !strings.Contains(warning, "runs 3.12") {
		t.Fatalf("warning = %q", warning)
	}
	if !strings.Contains(warning, "pip install -r requirements.txt") {
		t.Fatalf("warning must name the action: %q", warning)
	}
}

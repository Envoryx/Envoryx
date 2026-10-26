package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const instBackup = "manual-20261012-071500-ab12"

func instanceRoutes(restarted *atomic.Bool) map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/instance/backups": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"backups": []map[string]any{{"id": instBackup, "kind": "manual", "sizeBytes": 2048, "createdAt": "2026-10-12T05:15:00Z",
					"meta": map[string]any{"envoryx": "0.30.0", "note": "before the update"}}},
				"pendingRestore": map[string]any{"id": instBackup},
				"canRestart":     true,
				"offsiteTargets": []map[string]any{{"id": "t1", "name": "B2", "enabled": true}},
				"offsite":        map[string]any{instBackup: []map[string]any{{"targetName": "B2", "status": "done"}}},
			})
		},
		"POST /api/v1/instance/backups/{id}/restore": func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.PathValue("id"), "upload-") {
				w.WriteHeader(http.StatusForbidden)
				writeJSON(w, map[string]any{"error": map[string]any{"code": "session_required", "message": "API tokens can only restore instance backups this instance made"}})
				return
			}
			restarted.Store(true)
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"scheduled": r.PathValue("id"), "restarting": true})
		},
		"GET /api/v1/health": func(w http.ResponseWriter, r *http.Request) {
			// Before the restart the old process answers with a long uptime.
			uptime := 3600
			if restarted.Load() {
				uptime = 0
			}
			writeJSON(w, map[string]any{"status": "ok", "version": "0.30.0", "uptime": uptime})
		},
		"GET /api/v1/instance/backups/{id}/download": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="envoryx-`+r.PathValue("id")+`.tar.gz"`)
			_, _ = w.Write([]byte("archive"))
		},
		"POST /api/v1/instance/backups/upload": func(w http.ResponseWriter, r *http.Request) {
			// The fake server has read the body already; the test looks at what it kept.
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"backup": map[string]any{"id": "upload-20261012-080000-cd34", "sizeBytes": 7, "meta": map[string]any{"envoryx": "0.30.0"}}})
		},
	}
}

func TestInstanceBackupsList(t *testing.T) {
	srv := newFakeServer(t, instanceRoutes(new(atomic.Bool)))
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"instance", "backups"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{instBackup, "manual", "2.0 KiB", "0.30.0", "B2 ✓", "before the update", "cancel-restore"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

// The key comes from a file, never from the command line; --wait waits for a younger
// server process.
func TestInstanceRestoreSendsTheKeyAndWaits(t *testing.T) {
	old := restartPoll
	restartPoll = time.Millisecond
	t.Cleanup(func() { restartPoll = old })
	srv := newFakeServer(t, instanceRoutes(new(atomic.Bool)))
	c, out, _ := newTestCLI(t, srv, "envoryx-key-v1:abc\n")
	if err := c.run(context.Background(), []string{"instance", "restore", instBackup}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("without --yes: %v", err)
	}
	if err := c.run(context.Background(), []string{"instance", "restore", instBackup, "--yes", "--key-file", "-", "--wait"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]string
	if err := json.Unmarshal(srv.bodies["/api/v1/instance/backups/"+instBackup+"/restore"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["confirm"] != "restore" || sent["key"] != "envoryx-key-v1:abc" {
		t.Fatalf("sent %v", sent)
	}
	if !strings.Contains(out.String(), "Envoryx 0.30.0 is back") {
		t.Fatalf("output: %s", out.String())
	}
}

// An uploaded backup is restored from a browser session; the CLI says where.
func TestInstanceRestoreOfAnUploadPointsToTheBrowser(t *testing.T) {
	srv := newFakeServer(t, instanceRoutes(new(atomic.Bool)))
	c, _, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"instance", "restore", "upload-20261012-080000-cd34", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "sign in to the web UI and restore it there") {
		t.Fatalf("restore of an upload: %v", err)
	}
}

func TestInstanceDownloadAndUpload(t *testing.T) {
	srv := newFakeServer(t, instanceRoutes(new(atomic.Bool)))
	c, out, _ := newTestCLI(t, srv, "")
	dir := t.TempDir()
	target := filepath.Join(dir, "x.tar.gz")
	if err := c.run(context.Background(), []string{"instance", "download", instBackup, "-o", target}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// It holds the instance's database: owner only.
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	out.Reset()
	if err := c.run(context.Background(), []string{"instance", "upload", target}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "upload-20261012-080000-cd34") {
		t.Fatalf("output: %s", out.String())
	}
	sent := string(srv.bodies["/api/v1/instance/backups/upload"])
	if !strings.Contains(sent, `name="file"; filename="x.tar.gz"`) || !strings.Contains(sent, "archive") {
		t.Fatalf("upload body: %q", sent)
	}
}

// A download without -o takes the name the server suggests only when it is a plain file
// name, and never writes over a file that is there.
func TestDownloadNames(t *testing.T) {
	for disposition, want := range map[string]string{
		`attachment; filename="envoryx-20261012.tar.gz"`:          "envoryx-20261012.tar.gz",
		`attachment; filename=".bashrc"`:                          "fallback.tar",
		`attachment; filename="../../etc/passwd"`:                 "fallback.tar",
		`attachment; filename="/tmp/x"`:                           "fallback.tar",
		`attachment; filename="a b.tar"`:                          "fallback.tar",
		`attachment; filename="-rf"`:                              "fallback.tar",
		`attachment; filename="` + strings.Repeat("a", 202) + `"`: "fallback.tar",
		`attachment`: "fallback.tar",
		``:           "fallback.tar",
	} {
		if got := filenameFromResponse(disposition, "fallback.tar"); got != want {
			t.Errorf("%s: %q, want %q", disposition, got, want)
		}
	}

	srv := newFakeServer(t, instanceRoutes(new(atomic.Bool)))
	c, out, _ := newTestCLI(t, srv, "")
	dir := t.TempDir()
	t.Chdir(dir)
	name := "envoryx-" + instBackup + ".tar.gz"
	if err := c.run(context.Background(), []string{"instance", "download", instBackup}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != "archive" {
		t.Fatalf("download: %q %v (%s)", b, err, out.String())
	}
	// Again: the file is there, and only -o writes over it.
	if err := os.WriteFile(name, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := c.run(context.Background(), []string{"instance", "download", instBackup})
	if err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("over an existing file: %v", err)
	}
	if b, _ := os.ReadFile(name); string(b) != "mine" {
		t.Fatalf("the existing file was changed: %q", b)
	}
	if err := c.run(context.Background(), []string{"instance", "download", instBackup, "-o", name}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(name); string(b) != "archive" {
		t.Fatalf("-o: %q", b)
	}
}

// A copy on an offsite target is removed by its backup id, only with --yes.
func TestInstanceRemoteRemovesACopy(t *testing.T) {
	routes := instanceRoutes(new(atomic.Bool))
	routes["GET /api/v1/offsite/targets/t1/instance"] = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"backups": []map[string]any{{"key": "instance/manual-20261012-051500-ab12.tar.gz.age", "id": "manual-20261012-051500-ab12", "createdAt": "2026-10-12T05:15:00Z", "kind": "manual"}}})
	}
	routes["POST /api/v1/offsite/targets/t1/remove"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }
	srv := newFakeServer(t, routes)
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"instance", "remote", "--remove", "manual-20261012-051500-ab12"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("without --yes: %v", err)
	}
	if err := c.run(context.Background(), []string{"instance", "remote", "--remove", "manual-20261012-051500-ab12", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]string
	_ = json.Unmarshal(srv.bodies["/api/v1/offsite/targets/t1/remove"], &sent)
	if sent["key"] != "instance/manual-20261012-051500-ab12.tar.gz.age" || !strings.Contains(out.String(), "Removed the copy") {
		t.Fatalf("sent %v, output %s", sent, out)
	}
}

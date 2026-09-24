package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a repository with an origin remote and an envoryx.yml, and changes into
// a subdirectory of it the way people run commands from somewhere inside a checkout.
func gitRepo(t *testing.T, manifest string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "shop")
	for _, args := range [][]string{
		{"init", "-q", "-b", "feature/x", dir},
		{"-C", dir, "remote", "add", "origin", "https://git.example.com/acme/shop.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "envoryx.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "src")
	_ = os.MkdirAll(sub, 0o755)
	t.Chdir(sub)
	return dir
}

func TestUpCreatesAProjectFromTheCheckout(t *testing.T) {
	gitRepo(t, "version: 1\nphp: {version: \"8.4\"}\nsecrets: [STRIPE_SECRET]\n")
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/from-manifest": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{
				"plan":    map[string]any{"changes": []any{}, "missingSecrets": []string{}, "inSync": true},
				"project": map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": "shop", "slug": "shop", "hostnames": []string{"shop.test"}},
			})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"up", "--secret", "STRIPE_SECRET=sk_test"}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		YAML    string            `json:"yaml"`
		Name    string            `json:"name"`
		Git     gitSpec           `json:"git"`
		Secrets map[string]string `json:"secrets"`
		Start   bool              `json:"start"`
	}
	if err := json.Unmarshal(srv.bodies["/api/v1/projects/from-manifest"], &sent); err != nil {
		t.Fatal(err)
	}
	// No name in the manifest: the repository directory names the project.
	if sent.Name != "shop" || sent.Git.URL != "https://git.example.com/acme/shop.git" || sent.Git.Branch != "feature/x" || !sent.Start {
		t.Fatalf("request: %+v", sent)
	}
	if sent.Secrets["STRIPE_SECRET"] != "sk_test" || !strings.Contains(sent.YAML, "8.4") {
		t.Fatalf("request: %+v", sent)
	}
	if !strings.Contains(out.String(), "Created shop") || !strings.Contains(out.String(), "https://shop.test") {
		t.Fatalf("output: %s", out)
	}
}

func TestUpBringsAnExistingProjectInLine(t *testing.T) {
	gitRepo(t, "version: 1\nname: Blog\nnode: {version: \"24\"}\nredis: true\n")
	plan := map[string]any{"changes": []map[string]any{
		{"section": "redis", "action": "add", "to": "version: 8"},
		{"section": "worker", "item": "queue", "action": "remove", "skipped": "prune"},
	}, "missingSecrets": []string{}, "inSync": false}
	id := "22222222-2222-4222-8222-222222222222"
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/" + id + "/manifest/plan": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"plan": plan})
		},
		"POST /api/v1/projects/" + id + "/manifest/apply": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"plan": plan, "project": map[string]any{"id": id, "name": "Blog", "slug": "blog"}})
		},
	})

	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"up", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.requests {
		if strings.HasSuffix(r, "/apply") {
			t.Fatal("--dry-run must not apply")
		}
	}
	if !strings.Contains(out.String(), "+  redis") || !strings.Contains(out.String(), "needs --prune") {
		t.Fatalf("dry run output:\n%s", out)
	}

	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"up", "--prune"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/"+id+"/manifest/apply"], &sent)
	if sent["prune"] != true || sent["start"] != true || !strings.Contains(sent["yaml"].(string), "redis: true") {
		t.Fatalf("apply request: %v", sent)
	}
	if !strings.Contains(out.String(), "Blog (blog)") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestUpRefusesWhatTheServerCannotClone(t *testing.T) {
	dir := gitRepo(t, "version: 1\nname: fresh\n")
	srv := newFakeServer(t, nil)
	_ = exec.Command("git", "-C", dir, "remote", "set-url", "origin", "/srv/git/fresh.git").Run()
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"up"}); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("local remote: %v", err)
	}
	_ = exec.Command("git", "-C", dir, "remote", "remove", "origin").Run()
	if err := c.run(context.Background(), []string{"up"}); err == nil || !strings.Contains(err.Error(), "--git URL") {
		t.Fatalf("no remote: %v", err)
	}
	// A broken manifest is reported before the server is asked anything.
	_ = os.WriteFile(filepath.Join(dir, "envoryx.yml"), []byte("version: 1\nredsi: true\n"), 0o644)
	srv.requests = nil
	if err := c.run(context.Background(), []string{"up"}); err == nil || !strings.Contains(err.Error(), "unknown key redsi") || len(srv.requests) != 0 {
		t.Fatalf("broken manifest: %v %v", err, srv.requests)
	}
	if err := c.run(context.Background(), []string{"up", "--secret", "NOPE=x"}); err == nil {
		t.Fatal("an undeclared secret must be refused")
	}
}

func TestProjectManifestExport(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/" + id + "/manifest": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"yaml": "version: 1\nname: Acme Shop\n"})
		},
		"PUT /api/v1/projects/" + id + "/manifest/file": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"yaml": "version: 1\n"})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "manifest", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "version: 1\nname: Acme Shop\n" {
		t.Fatalf("stdout: %q", out)
	}
	file := filepath.Join(t.TempDir(), "envoryx.yml")
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "manifest", "acme-shop", "-o", file}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "Acme Shop") {
		t.Fatalf("file: %q", b)
	}
	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "manifest", "acme-shop", "--write"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Saved envoryx.yml") {
		t.Fatalf("write: %s", out)
	}
}

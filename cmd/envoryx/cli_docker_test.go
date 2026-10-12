package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func dockerRoutes() map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/docker/images/unused": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"images": []map[string]any{
				{"id": "sha256:0123456789abcdef0123", "tags": []string{"ghcr.io/envoryx/envoryx-php:8.2"}, "size": 512 << 20},
				{"id": "sha256:fedcba9876543210fedc", "size": 1 << 20},
			}})
		},
		"POST /api/v1/docker/images/prune": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"result": map[string]any{"removed": []map[string]any{{"tags": []string{"ghcr.io/envoryx/envoryx-php:8.2"}}}, "reclaimedBytes": 512 << 20}})
		},
		"GET /api/v1/system/reconcile": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"report": map[string]any{"orphans": []map[string]any{
				{"type": "volume", "id": "envoryx-old_db", "name": "envoryx-old_db", "projectName": "old"},
				{"type": "container", "id": "abcdef0123456789", "name": "envoryx-x-web", "projectName": "x", "unlabelled": true, "state": "exited"},
			}}})
		},
		"POST /api/v1/docker/orphans/remove": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"orphans": []any{}})
		},
	}
}

func TestDockerImagesAndPrune(t *testing.T) {
	srv := newFakeServer(t, dockerRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"docker", "images"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ghcr.io/envoryx/envoryx-php:8.2", "512.0 MiB", "fedcba987654", "513.0 MiB in all"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if err := c.run(context.Background(), []string{"docker", "prune"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("prune without --yes: %v", err)
	}
	out.Reset()
	if err := c.run(context.Background(), []string{"docker", "prune", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 image removed, 512.0 MiB freed") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestDockerOrphansAndRemove(t *testing.T) {
	srv := newFakeServer(t, dockerRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"docker", "orphans"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"envoryx-old_db", "x *", "without an instance label"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if err := c.run(context.Background(), []string{"docker", "remove", "container", "envoryx-x-web", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]string
	if err := json.Unmarshal(srv.bodies["/api/v1/docker/orphans/remove"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["type"] != "container" || sent["id"] != "abcdef0123456789" {
		t.Fatalf("sent %v", sent)
	}
	if err := c.run(context.Background(), []string{"docker", "remove", "volume", "envoryx-shop_db", "--yes"}); err == nil || !strings.Contains(err.Error(), "no orphaned volume") {
		t.Fatalf("not an orphan: %v", err)
	}
}

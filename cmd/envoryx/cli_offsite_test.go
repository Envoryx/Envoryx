package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBackupOffsiteCommands(t *testing.T) {
	pid := "11111111-1111-4111-8111-111111111111"
	targets := []map[string]any{{"id": "t1", "name": "B2", "enabled": true}, {"id": "t2", "name": "Box", "enabled": true}}
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/" + pid + "/backups": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"backups":        []map[string]any{{"id": "b1", "dir": "20260918-100000-abcd1234", "kind": "full", "sizeBytes": 2048, "createdAt": "2026-09-18T10:00:00Z", "meta": map[string]any{"source": "scheduled"}}},
				"offsite":        map[string]any{"b1": []map[string]any{{"targetId": "t1", "targetName": "B2", "status": "done"}, {"targetId": "t2", "targetName": "Box", "status": "failed", "error": "HTTP 403"}}},
				"offsiteTargets": targets,
			})
		},
		"POST /api/v1/projects/" + pid + "/backups/b1/offsite": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"offsite": []map[string]any{{"targetId": "t2", "targetName": "Box", "status": "pending"}}})
		},
		"GET /api/v1/projects/" + pid + "/offsite/t2": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"backups": []map[string]any{
				{"key": "projects/acme-shop/20260918-100000-abcd1234.full.scheduled.tar.age", "id": "20260918-100000-abcd1234", "createdAt": "2026-09-18T10:00:00Z", "kind": "full", "source": "scheduled", "sizeBytes": 3000, "encrypted": true},
				{"key": "projects/acme-shop/20260901-020000-00000000.full.scheduled.tar.age", "id": "20260901-020000-00000000", "createdAt": "2026-09-01T02:00:00Z", "kind": "full", "source": "scheduled", "sizeBytes": 2900, "encrypted": true},
			}})
		},
		"POST /api/v1/projects/" + pid + "/offsite/t2/fetch": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"backup": map[string]any{"id": "b9", "kind": "full", "sizeBytes": 2900, "meta": map[string]any{}}})
		},
	})

	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"backup", "list", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "OFFSITE") || !strings.Contains(out.String(), "B2 ✓, Box failed") {
		t.Fatalf("list:\n%s", out)
	}

	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"backup", "offsite", "acme-shop", "b1", "--target", "box"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string][]string
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/"+pid+"/backups/b1/offsite"], &sent)
	if len(sent["targets"]) != 1 || sent["targets"][0] != "t2" || !strings.Contains(out.String(), "Copying b1 to Box") {
		t.Fatalf("offsite: %v %s", sent, out)
	}

	// Two targets: remote and fetch need to know which.
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"backup", "remote", "acme-shop"}); err == nil || !strings.Contains(err.Error(), "--target") {
		t.Fatalf("ambiguous target: %v", err)
	}
	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"backup", "remote", "acme-shop", "--target", "Box"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "also here") || !strings.Contains(out.String(), "20260901-020000-00000000") {
		t.Fatalf("remote:\n%s", out)
	}
	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"backup", "fetch", "acme-shop", "20260901-020000-00000000", "--target", "t2"}); err != nil {
		t.Fatal(err)
	}
	var fetched map[string]string
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/"+pid+"/offsite/t2/fetch"], &fetched)
	if fetched["key"] != "projects/acme-shop/20260901-020000-00000000.full.scheduled.tar.age" || !strings.Contains(out.String(), "envoryx backup restore acme-shop b9") {
		t.Fatalf("fetch: %v %s", fetched, out)
	}
}

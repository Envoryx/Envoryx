package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdditionalDatabasesFromTheCLI(t *testing.T) {
	var snapshotQuery, cloneQuery string
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"project": map[string]any{"id": "77777777-7777-4777-8777-777777777777", "name": "Shop", "slug": "shop"}})
		},
		"POST /api/v1/projects/{id}/database/snapshots": func(w http.ResponseWriter, r *http.Request) {
			snapshotQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"snapshot": map[string]any{"id": "s1", "sizeBytes": 10}})
		},
		"POST /api/v1/projects/{id}/database/clone": func(w http.ResponseWriter, r *http.Request) {
			cloneQuery = r.URL.RawQuery
			writeJSON(w, map[string]any{"clone": map[string]any{"source": "acme-shop", "database": "blog"}})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "create", "Shop", "--database", "mariadb", "--add-database", "analytics=postgres:17", "--add-database", "legacy=mysql"}); err != nil {
		t.Fatal(err)
	}
	var sent createRequest
	if err := json.Unmarshal(srv.bodies["/api/v1/projects"], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Databases) != 2 || sent.Databases[0].Name != "analytics" || sent.Databases[0].Type != "postgresql" || sent.Databases[0].Version != "17" || sent.Databases[1].Type != "mysql" {
		t.Fatalf("databases sent: %+v", sent.Databases)
	}
	if err := c.run(context.Background(), []string{"project", "create", "Shop", "--add-database", "analytics"}); err == nil {
		t.Fatal("--add-database without a type must be refused")
	}

	if err := c.run(context.Background(), []string{"db", "snapshot", "blog", "--db", "analytics"}); err != nil {
		t.Fatal(err)
	}
	if snapshotQuery != "db=analytics" || !strings.Contains(out.String(), "the database analytics of blog") {
		t.Fatalf("query %q, output %s", snapshotQuery, out)
	}
	if err := c.run(context.Background(), []string{"db", "clone", "blog", "--from", "acme-shop", "--db", "analytics", "--source-db", "primary", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/22222222-2222-4222-8222-222222222222/database/clone"], &clone)
	if cloneQuery != "db=analytics" || clone["sourceDb"] != "" {
		t.Fatalf("clone query %q body %v", cloneQuery, clone)
	}
}

package api_test

import (
	"net/http"
	"slices"
	"testing"
)

// An addon made for particular databases is offered only to projects with one of them:
// phpMyAdmin not to a PostgreSQL project, pgAdmin not to a MariaDB one.
func TestProjectAddonsOfferedByDatabase(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	for _, src := range []string{
		"name: phpmyadmin\ndatabases: [mariadb, mysql]\nversions:\n  - version: \"5.2\"\n    image: phpmyadmin:5.2\n",
		"name: pgadmin\ndatabases: [postgresql]\nversions:\n  - version: \"9\"\n    image: dpage/pgadmin4:9\n",
		"name: soketi\nversions:\n  - version: \"1\"\n    image: quay.io/soketi/soketi:1.6-16-debian\n",
	} {
		if r := a.do(http.MethodPost, "/api/v1/addons", map[string]any{"source": src}, true); r.status != http.StatusOK && r.status != http.StatusCreated {
			t.Fatalf("install: %d %s", r.status, r.raw)
		}
	}
	offered := func(db string) []string {
		t.Helper()
		create := map[string]any{"name": "Shop " + db + "x", "php": map[string]any{"version": "8.4"}}
		if db != "" {
			create["database"] = map[string]any{"type": db}
		}
		r := a.do(http.MethodPost, "/api/v1/projects", create, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create: %d %s", r.status, r.raw)
		}
		id := r.body["project"].(map[string]any)["id"].(string)
		r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/addons", nil, false)
		var names []string
		for _, av := range r.body["available"].([]any) {
			names = append(names, av.(map[string]any)["name"].(string))
		}
		slices.Sort(names)
		return names
	}
	for db, want := range map[string][]string{
		"mariadb":    {"phpmyadmin", "soketi"},
		"postgresql": {"pgadmin", "soketi"},
		"":           {"soketi"},
	} {
		if got := offered(db); !slices.Equal(got, want) {
			t.Errorf("database %q: offered %v, want %v", db, got, want)
		}
	}
}

package project

import (
	"encoding/json"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

// shape builds a project from the services it has; devServer selects the Node mode.
func shape(php, phpEnabled, node, devServer bool) store.Project {
	p := store.Project{Slug: "shop", Services: []store.ProjectService{{Kind: store.ServiceWeb, Variant: "caddy", Enabled: true, Image: "caddy:2-alpine"}}}
	if php {
		p.Services = append(p.Services, store.ProjectService{Kind: store.ServicePHP, Enabled: phpEnabled, Image: "ghcr.io/envoryx/envoryx-php:8.4"})
	}
	if node {
		cfg, _ := json.Marshal(map[string]any{"devServer": devServer, "preset": "vite", "port": 5173})
		p.Services = append(p.Services, store.ProjectService{Kind: store.ServiceNode, Enabled: true, Image: "ghcr.io/envoryx/envoryx-node:24", Config: cfg})
	}
	return p
}

func TestAppHelpers(t *testing.T) {
	cases := []struct {
		name       string
		p          store.Project
		app        store.ServiceKind
		serves     string
		nodeServes bool
	}{
		{"php+node dev", shape(true, true, true, true), store.ServicePHP, "php", false},
		{"php only", shape(true, true, false, false), store.ServicePHP, "php", false},
		{"node dev", shape(false, false, true, true), store.ServiceNode, "node", true},
		{"node idle", shape(false, false, true, false), store.ServiceNode, "static", false},
		{"static", shape(false, false, false, false), "", "static", false},
		{"disabled php + node dev", shape(true, false, true, true), store.ServiceNode, "node", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := AppKind(tc.p)
			if ok != (tc.app != "") || kind != tc.app {
				t.Errorf("AppKind = %q,%v want %q", kind, ok, tc.app)
			}
			if svc := appService(tc.p); (svc == nil) == (tc.app != "") || (svc != nil && svc.Kind != tc.app) {
				t.Errorf("appService = %+v", svc)
			}
			if got := Serves(tc.p); got != tc.serves {
				t.Errorf("Serves = %q want %q", got, tc.serves)
			}
			cfg, ok := nodeServesApp(tc.p)
			if ok != tc.nodeServes {
				t.Errorf("nodeServesApp = %v want %v", ok, tc.nodeServes)
			}
			if ok && (cfg.Port != 5173 || cfg.PackageManager != "npm" || cfg.Script != "dev") {
				t.Errorf("config must come back normalised: %+v", cfg)
			}
		})
	}
}

func TestToolImage(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		p    store.Project
		want string
	}{
		{"php wins", shape(true, true, true, true), "ghcr.io/envoryx/envoryx-php:8.4"},
		{"node", shape(false, false, true, false), "ghcr.io/envoryx/envoryx-node:24"},
		{"disabled php falls through to node", shape(true, false, true, false), "ghcr.io/envoryx/envoryx-node:24"},
	}
	for _, tc := range cases {
		got, err := e.m.toolImage(tc.p)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	// Static projects fall back to the catalogue's default Node image.
	got, err := e.m.toolImage(shape(false, false, false, false))
	if err != nil {
		t.Fatal(err)
	}
	def, _ := e.m.catalog.Resolve("node", "")
	if got != def.Image || got == "" {
		t.Fatalf("static fallback: got %q want %q", got, def.Image)
	}
}

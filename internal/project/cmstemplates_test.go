package project

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

func TestCMSTemplatesScaffoldWithThePHPConfiguration(t *testing.T) {
	cases := []struct {
		id, pkg, docroot string
		steps            int
	}{
		{"drupal", "drupal/recommended-project", "web", 3},
		{"typo3", "typo3/cms-base-distribution", "public", 2},
		{"shopware", "shopware/production", "public", 1},
		{"craft", "craftcms/craft", "web", 2},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			e := newEnv(t)
			var runs []docker.ContainerSpec
			e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
				runs = append(runs, spec)
				return docker.ExecResult{}, nil
			}
			v, err := e.m.Create(context.Background(), CreateRequest{Name: "Site", Template: c.id, PHP: &PHPRequest{Version: "8.4"}, Database: &DatabaseRequest{Type: "mariadb"}})
			if err != nil {
				t.Fatal(err)
			}
			if v.Project.Docroot != c.docroot || len(runs) != c.steps {
				t.Fatalf("docroot %q, %d steps", v.Project.Docroot, len(runs))
			}
			if got := strings.Join(runs[0].Cmd, " "); got != "composer create-project "+c.pkg+" . --no-interaction --prefer-dist" {
				t.Fatalf("first step: %s", got)
			}
			// The project's php.ini is there: create-project checks the extensions it lists.
			ini := filepath.Join(e.cfgDir, "projects", v.Project.ID, "php", "zz-envoryx.ini")
			if _, err := os.Stat(ini); err != nil {
				t.Fatalf("php.ini not written before the template: %v", err)
			}
			for _, r := range runs {
				if !slices.ContainsFunc(r.Mounts, func(m docker.MountSpec) bool { return m.Target == phpIniTarget && m.ReadOnly }) {
					t.Fatalf("%v runs without the project's php.ini: %+v", r.Cmd, r.Mounts)
				}
			}
		})
	}
}

func TestShopwareGetsMemoryAndItsURL(t *testing.T) {
	e := newEnv(t)
	e.engine.OneShotHandler = func(docker.ContainerSpec) (docker.ExecResult, error) { return docker.ExecResult{}, nil }
	v, err := e.m.Create(context.Background(), CreateRequest{Name: "Store", Template: "shopware", PHP: &PHPRequest{Version: "8.4"}, Database: &DatabaseRequest{Type: "mariadb"}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg runtime.PHPConfig
	_ = json.Unmarshal(v.Project.Service(store.ServicePHP).Config, &cfg)
	if cfg.MemoryLimit != "1G" {
		t.Fatalf("memory limit: %q", cfg.MemoryLimit)
	}
	env, _ := os.ReadFile(filepath.Join(e.projDir, "store", ".env.local"))
	if !strings.Contains(string(env), "APP_URL=${ENVORYX_URL}") {
		t.Fatalf(".env.local: %q", env)
	}
}

func TestEnvoryxURLIsInjected(t *testing.T) {
	e := newEnv(t)
	planner := NewPlanner(Paths{BaseDomain: "test", ProxyHTTPSPort: 443}, runtime.Default())
	if u := planner.ProjectURL(store.Project{Slug: "shop"}); u != "https://shop.test" {
		t.Fatalf("url: %q", u)
	}
	planner = NewPlanner(Paths{BaseDomain: "lan", ProxyHTTPPort: 8080}, runtime.Default())
	if u := planner.ProjectURL(store.Project{Slug: "shop"}); u != "http://shop.lan:8080" {
		t.Fatalf("url: %q", u)
	}
	planner = NewPlanner(Paths{PublicHost: "192.168.1.5"}, runtime.Default())
	if u := planner.ProjectURL(store.Project{Slug: "shop", HTTPPort: 20000}); u != "http://192.168.1.5:20000" {
		t.Fatalf("url: %q", u)
	}
	_ = e
}

func TestPostgreSQLSwitchesOnItsPHPDriver(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	has := func(p store.Project) bool {
		var cfg runtime.PHPConfig
		_ = json.Unmarshal(p.Service(store.ServicePHP).Config, &cfg)
		return slices.Contains(cfg.Extensions, "pdo_pgsql")
	}
	req := phpRequest("Api", false)
	req.Database = &DatabaseRequest{Type: "postgresql"}
	v, err := e.m.Create(ctx, req)
	if err != nil || !has(v.Project) {
		t.Fatalf("created with PostgreSQL: %v %+v", err, v.Project.Service(store.ServicePHP))
	}
	v, err = e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil || has(v.Project) {
		t.Fatalf("MariaDB needs no pdo_pgsql: %v", err)
	}
	v, err = e.m.Update(ctx, v.Project.ID, UpdateRequest{Databases: map[string]DatabaseUpdate{"reports": {Enabled: true, Type: "postgresql"}}})
	if err != nil || !has(v.Project) {
		t.Fatalf("an added PostgreSQL database: %v", err)
	}
}

// The install scripts of the CMS actions are shell code run by sh -c; a syntax error
// would only show when someone clicks the action.
func TestInstallScriptsAreValidShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for name, script := range map[string]string{"drush": drushInstallScript, "typo3": typo3SetupScript, "craft": craftInstallScript} {
		if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
	for _, id := range []string{"drush:site-install", "typo3:setup", "craft:install", "shopware:install"} {
		a, ok := findAction(id)
		if !ok || !a.Destructive {
			t.Errorf("action %s: %+v", id, a)
		}
	}
}

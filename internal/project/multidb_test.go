package project

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// multiDBRequest is a PHP project with a MariaDB primary and a PostgreSQL named analytics.
func multiDBRequest(name string, start bool) CreateRequest {
	req := dbRequest(name, false)
	req.Start = start
	req.Databases = []NamedDatabaseRequest{{Name: "analytics", DatabaseRequest: DatabaseRequest{Type: "postgresql", ExposePort: true}}}
	return req
}

// dumpRecorder answers dumps with a line naming the container and records imports.
type dumpRecorder struct {
	mu       sync.Mutex
	restored map[string]string // container → what was imported
}

func (d *dumpRecorder) handler(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch cmd[0] {
	case "mariadb-dump", "pg_dump", "mysqldump":
		return "-- dump of " + container + "\n", 0, nil
	case "mariadb", "psql", "mysql":
		if d.restored == nil {
			d.restored = map[string]string{}
		}
		d.restored[container] += string(stdin)
		return "", 0, nil
	}
	return "", 0, nil
}

func TestCreateProjectWithAdditionalDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, multiDBRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	p := view.Project
	extra := p.Service(store.DatabaseKind("analytics"))
	if extra == nil || extra.Variant != "postgresql" || extra.Kind != "db-analytics" {
		t.Fatalf("additional database: %+v", extra)
	}
	var cfg runtime.DatabaseConfig
	_ = json.Unmarshal(extra.Config, &cfg)
	if cfg.Database != "shop" || cfg.Username != "shop" || cfg.HostPort == 0 {
		t.Fatalf("config: %+v", cfg)
	}
	c, ok := e.engine.Container("envoryx-shop-db-analytics")
	if !ok || c.State != "running" || !slices.Equal(c.Spec.NetworkAlias, []string{"analytics"}) || c.Spec.Mounts[0].Source != "envoryx-shop-db-analytics" {
		t.Fatalf("container: %+v", c)
	}
	if c.Spec.Ports[0].ContainerPort != 5432 || c.Spec.Ports[0].HostPort != cfg.HostPort {
		t.Fatalf("ports: %+v", c.Spec.Ports)
	}
	primary, _ := e.engine.Container("envoryx-shop-database")
	if !slices.Contains(primary.Spec.NetworkAlias, "database") {
		t.Fatalf("primary aliases: %v", primary.Spec.NetworkAlias)
	}
	php, _ := e.engine.Container("envoryx-shop-php")
	env := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"DB_HOST=database", "DB_PORT=3306", "ANALYTICS_DB_HOST=analytics", "ANALYTICS_DB_PORT=5432", "ANALYTICS_DB_CONNECTION=pgsql",
		"ANALYTICS_DB_PASSWORD=" + cfg.Password, "ANALYTICS_DATABASE_URL=pgsql://shop:" + cfg.Password + "@analytics:5432/shop"} {
		if !strings.Contains(env, want) {
			t.Errorf("php env misses %q", want)
		}
	}

	infos, err := e.m.Databases(ctx, p.ID)
	if err != nil || len(infos) != 2 || infos[0].Name != "" || infos[1].Name != "analytics" || infos[1].Host != "analytics" || infos[1].State != "running" || !infos[1].VolumeExists {
		t.Fatalf("databases: %+v %v", infos, err)
	}
	creds, err := e.m.DatabaseCredentials(ctx, p.ID, "analytics")
	if err != nil || creds.Host != "analytics" || creds.Port != 5432 || !strings.Contains(creds.URL, "@analytics:5432/") {
		t.Fatalf("credentials: %+v %v", creds, err)
	}
	if _, err := e.m.DatabaseInfo(ctx, p.ID, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown database: %v", err)
	}
}

func TestAdditionalDatabaseNames(t *testing.T) {
	for _, bad := range []string{"", "Analytics", "database", "redis", "mariadb", "1db", "a_b", "a--b", "a-", strings.Repeat("a", 25)} {
		if err := ValidateDatabaseServiceName(bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, good := range []string{"analytics", "legacy-db", "db2", "a"} {
		if err := ValidateDatabaseServiceName(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	e := newEnv(t)
	req := multiDBRequest("Shop", false)
	req.Databases = append(req.Databases, req.Databases[0])
	if _, err := e.m.Create(context.Background(), req); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("two databases with one name: %v", err)
	}
}

func TestAddChangeAndRemoveAdditionalDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	view, err = e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"legacy": {Enabled: true, Type: "mysql"}}})
	if err != nil {
		t.Fatal(err)
	}
	if svc := view.Project.Service(store.DatabaseKind("legacy")); svc == nil || svc.Variant != "mysql" {
		t.Fatalf("added: %+v", view.Project.Services)
	}
	if c, ok := e.engine.Container("envoryx-shop-db-legacy"); !ok || c.State != "running" {
		t.Fatalf("container: %+v", c)
	}
	php, _ := e.engine.Container("envoryx-shop-php")
	if !strings.Contains(strings.Join(php.Spec.Env, "\n"), "LEGACY_DB_HOST=legacy") {
		t.Fatal("the application containers must be recreated with the new variables")
	}
	// Publishing its port leaves the primary alone.
	if _, err := e.m.SetDatabaseExposed(ctx, id, "legacy", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-shop-db-legacy"); len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 3306 {
		t.Fatalf("legacy ports: %+v", c.Spec.Ports)
	}
	if c, _ := e.engine.Container("envoryx-shop-database"); len(c.Spec.Ports) != 1 {
		t.Fatalf("primary ports changed: %+v", c.Spec.Ports)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"legacy": {Enabled: false}}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("removing without removeData: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"ghost": {Enabled: false, RemoveData: true}}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing an unknown database: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"web": {Enabled: true}}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("a reserved name: %v", err)
	}
	view, err = e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"legacy": {Enabled: false, RemoveData: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.DatabaseKind("legacy")) != nil {
		t.Fatal("service still there")
	}
	if _, ok := e.engine.Container("envoryx-shop-db-legacy"); ok {
		t.Fatal("container still there")
	}
	if slices.Contains(e.engine.VolumeNames(), "envoryx-shop-db-legacy") {
		t.Fatal("volume still there")
	}
	if !slices.Contains(e.engine.VolumeNames(), "envoryx-shop-database") {
		t.Fatal("the primary's volume must stay")
	}
}

func TestBackupSnapshotAndCloneOfAdditionalDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rec := &dumpRecorder{}
	e.engine.StreamHandler = rec.handler
	view, err := e.m.Create(ctx, multiDBRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	p := view.Project

	b, err := e.m.CreateBackup(ctx, p.ID, BackupOptions{Database: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.Meta.Database == nil || len(b.Meta.Databases) != 1 || b.Meta.Databases[0].DB != "analytics" || b.Meta.Databases[0].Type != "postgresql" || b.Kind != "database" {
		t.Fatalf("backup meta: %+v", b.Meta)
	}
	dir := filepath.Join(e.cfgDir, "backups", "shop", b.Dir)
	for _, f := range []string{"database.sql.gz", "database-analytics.sql.gz"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if members := backupMembersIn(dir); !slices.Equal(members[:3], []string{"backup.json", "database.sql.gz", "database-analytics.sql.gz"}) {
		t.Fatalf("archive members: %v", members)
	}
	if _, err := e.m.RestoreBackup(ctx, p.ID, b.ID, RestoreOptions{Database: true, Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.restored["envoryx-shop-database"], "dump of envoryx-shop-database") || !strings.Contains(rec.restored["envoryx-shop-db-analytics"], "dump of envoryx-shop-db-analytics") {
		t.Fatalf("restored: %v", rec.restored)
	}

	// A snapshot is of one database; listing and restoring go by that database.
	snap, err := e.m.CreateSnapshot(ctx, p.ID, "analytics", "before")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta.Database != nil || len(snap.Meta.Databases) != 1 {
		t.Fatalf("snapshot meta: %+v", snap.Meta)
	}
	list, _ := e.m.ListSnapshots(ctx, p.ID, "analytics")
	primaryList, _ := e.m.ListSnapshots(ctx, p.ID, "")
	if !slices.ContainsFunc(list, func(x BackupInfo) bool { return x.ID == snap.ID }) || slices.ContainsFunc(primaryList, func(x BackupInfo) bool { return x.ID == snap.ID }) {
		t.Fatalf("snapshot lists: analytics %d, primary %d", len(list), len(primaryList))
	}
	rec.restored = nil
	if _, err := e.m.RestoreSnapshot(ctx, p.ID, "analytics", snap.ID, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, touched := rec.restored["envoryx-shop-database"]; touched || rec.restored["envoryx-shop-db-analytics"] == "" {
		t.Fatalf("snapshot restore touched: %v", rec.restored)
	}
	if _, err := e.m.RestoreSnapshot(ctx, p.ID, "", snap.ID, "shop"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("restoring a snapshot of analytics into the primary: %v", err)
	}

	// A second project takes analytics' data; flavours must match.
	other, err := e.m.Create(ctx, CreateRequest{Name: "Report", Databases: []NamedDatabaseRequest{{Name: "analytics", DatabaseRequest: DatabaseRequest{Type: "postgresql"}}}, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	rec.restored = nil
	res, err := e.m.CloneDatabase(ctx, other.Project.ID, CloneDatabaseRequest{Source: p.ID, DB: "analytics", Confirm: "report"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "shop" || !strings.Contains(rec.restored["envoryx-report-db-analytics"], "dump of envoryx-shop-db-analytics") {
		t.Fatalf("clone: %+v %v", res, rec.restored)
	}
	primary := ""
	if _, err := e.m.CloneDatabase(ctx, other.Project.ID, CloneDatabaseRequest{Source: p.ID, DB: "analytics", SourceDB: &primary, Confirm: "report"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("MariaDB into PostgreSQL: %v", err)
	}
}

func TestDuplicateAndRenameCarryAdditionalDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rec := &dumpRecorder{}
	e.engine.StreamHandler = rec.handler
	view, err := e.m.Create(ctx, multiDBRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := e.m.Duplicate(ctx, view.Project.ID, allParts("Shop Test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-shop-test-db-analytics"); !ok {
		t.Fatalf("containers: %v", e.engine.ContainerNames())
	}
	if !strings.Contains(rec.restored["envoryx-shop-test-db-analytics"], "dump of envoryx-shop-db-analytics") {
		t.Fatalf("copied: %v", rec.restored)
	}
	var src, dst runtime.DatabaseConfig
	_ = json.Unmarshal(view.Project.Service(store.DatabaseKind("analytics")).Config, &src)
	_ = json.Unmarshal(cp.Project.Service(store.DatabaseKind("analytics")).Config, &dst)
	if dst.HostPort == 0 || dst.HostPort == src.HostPort {
		t.Fatalf("the copy needs a port of its own: %d vs %d", dst.HostPort, src.HostPort)
	}

	res, err := e.m.Rename(ctx, cp.Project.ID, RenameRequest{Name: "Staging", Confirm: "shop-test"})
	if err != nil {
		t.Fatal(err)
	}
	renamed, _ := e.m.Get(ctx, res.View.Project.ID)
	var cfg runtime.DatabaseConfig
	_ = json.Unmarshal(renamed.Project.Service(store.DatabaseKind("analytics")).Config, &cfg)
	if cfg.Database != "staging" || cfg.Username != "staging" {
		t.Fatalf("the additional database keeps the old names: %+v", cfg)
	}
	if _, ok := e.engine.Container("envoryx-staging-db-analytics"); !ok {
		t.Fatalf("containers: %v", e.engine.ContainerNames())
	}
	if !slices.Contains(e.engine.VolumeNames(), "envoryx-staging-db-analytics") {
		t.Fatalf("volumes: %v", e.engine.VolumeNames())
	}
}

func TestManifestCarriesAdditionalDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, multiDBRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	mf, err := e.m.ExportManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := mf.Databases["analytics"]; !ok || d.Type != "postgresql" || !d.ExposePort || mf.Database == nil || mf.Database.Type != "mariadb" {
		t.Fatalf("export: %+v %+v", mf.Database, mf.Databases)
	}
	raw, err := manifest.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "databases:\n  analytics:\n    type: postgresql") {
		t.Fatalf("yaml:\n%s", raw)
	}

	// In sync with itself; a new name is an addition, a missing one a removal (with prune).
	plan, err := e.m.PlanManifest(ctx, id, mf, ManifestOptions{})
	if err != nil || !plan.InSync {
		t.Fatalf("plan of the export: %+v %v", plan, err)
	}
	mf.Databases["legacy"] = manifest.Database{Type: "mysql"}
	delete(mf.Databases, "analytics")
	res, err := e.m.ApplyManifest(ctx, id, mf, ManifestOptions{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	sections := []string{}
	for _, c := range res.Plan.Changes {
		sections = append(sections, c.Section+":"+c.Action)
	}
	if !slices.Contains(sections, "databases.legacy:add") || !slices.Contains(sections, "databases.analytics:remove") {
		t.Fatalf("changes: %v", sections)
	}
	after, _ := e.m.Get(ctx, id)
	if after.Project.Service(store.DatabaseKind("legacy")) == nil || after.Project.Service(store.DatabaseKind("analytics")) != nil {
		t.Fatalf("services after apply: %+v", after.Project.Services)
	}

	// A manifest names additional databases for a new project too.
	mf2, err := manifest.Parse([]byte("version: 1\nphp: {version: \"8.4\"}\ndatabases:\n  reports:\n    type: postgres\n"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := e.m.CreateFromManifest(ctx, mf2, ManifestCreateRequest{Name: "Reports"})
	if err != nil {
		t.Fatal(err)
	}
	if svc := created.View.Project.Service(store.DatabaseKind("reports")); svc == nil || svc.Variant != "postgresql" {
		t.Fatalf("created: %+v", created.View.Project.Services)
	}
}

package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

const brokenKernel = "6.19.0-31-generic"

func TestMongoVersionsOnAffectedKernel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Set up on an older kernel, where 7.0 and 8.0 still start.
	e.engine.KernelVersion = "6.8.0-1-pve"
	req := phpRequest("Shop", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "7"}
	req.Databases = []NamedDatabaseRequest{{Name: "analytics", DatabaseRequest: DatabaseRequest{Type: "mongodb", Version: "8"}}}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Status.Warnings) != 0 {
		t.Fatalf("warnings on an older kernel: %v", view.Status.Warnings)
	}

	// The host kernel moves to 6.19: both databases are named with the way forward from
	// the data they have.
	e.engine.KernelVersion = brokenKernel
	e.m.noteKernel(brokenKernel)
	want := []string{
		"cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic), and MongoDB 8.2 cannot take over its data; export it on a host with an older kernel, or remove and re-add the database (its data is lost)",
		"cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); upgrade the database to MongoDB 8.2, which takes over its data",
	}
	view, err = e.m.Get(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !slices.Contains(view.Status.Warnings, w) {
			t.Fatalf("missing %q in %v", w, view.Status.Warnings)
		}
	}
	views, err := e.m.List(ctx)
	if err != nil || len(views) != 1 || !slices.Contains(views[0].Status.Warnings, want[0]) {
		t.Fatalf("list warnings: %v %v", views, err)
	}

	// The project keeps its versions: an unrelated change still saves.
	env := []EnvVarRequest{{Key: "APP_ENV", Value: "production"}}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Env: &env}); err != nil {
		t.Fatalf("unrelated update refused: %v", err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Type: "mongodb", Version: "7", ExposePort: true}}); err != nil {
		t.Fatalf("update keeping the version refused: %v", err)
	}

	// Newly choosing a broken version is refused, with the reason.
	_, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Databases: map[string]DatabaseUpdate{"reports": {Enabled: true, Type: "mongodb", Version: "8"}}})
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "cannot start MongoDB 8.0 on Linux kernel 6.19") {
		t.Fatalf("adding mongo 8.0: %v", err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Databases: map[string]DatabaseUpdate{"reports": {Enabled: true, Type: "mongodb", Version: "8.2"}}}); err != nil {
		t.Fatalf("adding mongo 8.2: %v", err)
	}
	req = phpRequest("Blog", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	if _, err := e.m.Preview(ctx, req); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("preview with mongo 8.0: %v", err)
	}
	if _, err := e.m.Create(ctx, req); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "switch to MongoDB 8.2") {
		t.Fatalf("create with mongo 8.0: %v", err)
	}
	req.Database.Version = "8.2"
	if _, err := e.m.Create(ctx, req); err != nil {
		t.Fatalf("create with mongo 8.2: %v", err)
	}
}

func TestUnknownKernelLocksNothing(t *testing.T) {
	e := newEnv(t)
	req := phpRequest("Shop", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	view, err := e.m.Create(context.Background(), req)
	if err != nil || len(view.Status.Warnings) != 0 {
		t.Fatalf("unknown kernel: %v %v", view.Status.Warnings, err)
	}
}

func TestMongoWayForwardOnAffectedKernel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.KernelVersion = "6.8.0-1-pve"
	req := phpRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	req.Databases = []NamedDatabaseRequest{{Name: "old", DatabaseRequest: DatabaseRequest{Type: "mongodb", Version: "7"}}}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// 8.2 opens 8.0 data but not 7.0 data, on any kernel.
	_, err = e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"old": {Enabled: true, Type: "mongodb", Version: "8.2"}}})
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "cannot upgrade an existing data directory from 7 to 8.2 in place") {
		t.Fatalf("7.0 to 8.2 on an older kernel: %v", err)
	}

	e.engine.KernelVersion = brokenKernel
	e.m.noteKernel(brokenKernel)
	view, err = e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); upgrade the database to MongoDB 8.2, which takes over its data",
		"cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic), and MongoDB 8.2 cannot take over its data; export it on a host with an older kernel, or remove and re-add the database (its data is lost)",
	} {
		if !slices.Contains(view.Status.Warnings, w) {
			t.Fatalf("missing %q in %v", w, view.Status.Warnings)
		}
	}

	// A restart leaves the two servers that would only crash-loop stopped and starts the rest.
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatalf("restart: %v", err)
	}
	for name, want := range map[string]string{"envoryx-shop-php": "running", "envoryx-shop-database": "exited", "envoryx-shop-db-old": "exited"} {
		if c, _ := e.engine.Container(name); c.State != want {
			t.Fatalf("%s is %q, want %q", name, c.State, want)
		}
	}

	// 7.0 has no way to 8.2 here, and the refusal says so instead of "export".
	_, err = e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"old": {Enabled: true, Type: "mongodb", Version: "8.2"}}})
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "MongoDB 8.2 cannot take over its data; export it on a host with an older kernel") {
		t.Fatalf("7.0 to 8.2 on 6.19: %v", err)
	}

	// 8.0 to 8.2 works: no dump is possible, so the backup copies the data volumes.
	var copied []string
	e.engine.OneShotStreamHandler = func(spec docker.ContainerSpec, stdin []byte) (string, int, error) {
		if spec.Image != addonVolumeTool {
			return "", 0, nil
		}
		// The data volumes are named like their containers.
		if c, _ := e.engine.Container(spec.Mounts[0].Source); c.State == "running" {
			t.Errorf("%s runs while its volume is used", spec.Mounts[0].Source)
		}
		if spec.Cmd[0] == "tar" {
			copied = append(copied, spec.Mounts[0].Source)
			return "TARBYTES", 0, nil
		}
		copied = append(copied, "restore "+spec.Mounts[0].Source+":"+string(stdin))
		return "", 0, nil
	}
	view, err = e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Type: "mongodb", Version: "8.2"}})
	if err != nil {
		t.Fatalf("8.0 to 8.2: %v", err)
	}
	if v := view.Project.Service(store.ServiceDatabase); v.Version != "8.2" || v.Image != "mongo:8.2" {
		t.Fatalf("upgraded service: %+v", v)
	}
	if c, _ := e.engine.Container("envoryx-shop-database"); c.Spec.Image != "mongo:8.2" || c.Spec.Mounts[0].Source != "envoryx-shop-database" || c.State != "running" {
		t.Fatalf("upgraded container: %s %v %s", c.Spec.Image, c.Spec.Mounts, c.State)
	}
	if !slices.Equal(copied, []string{"envoryx-shop-database", "envoryx-shop-db-old"}) {
		t.Fatalf("volume copies: %v", copied)
	}
	backups, err := e.m.ListBackups(ctx, id)
	if err != nil || len(backups) != 1 || len(backups[0].Meta.DatabaseVolumes) != 2 {
		t.Fatalf("backups: %+v %v", backups, err)
	}
	b := backups[0]
	want := []VolumeCopyMeta{
		{DB: "", Type: "mongodb", Version: "8", File: "database.volume.tar.gz", Bytes: b.Meta.DatabaseVolumes[0].Bytes},
		{DB: "old", Type: "mongodb", Version: "7", File: "database-old.volume.tar.gz", Bytes: b.Meta.DatabaseVolumes[1].Bytes},
	}
	if b.Meta.Source != "upgrade" || b.Kind != "database" || b.Meta.Database != nil || !slices.Equal(b.Meta.DatabaseVolumes, want) || want[0].Bytes == 0 {
		t.Fatalf("upgrade backup: %+v", b)
	}

	// The copy goes back into the volume: 8.2 opens the 8.0 files it holds.
	copied = nil
	if _, err := e.m.RestoreBackup(ctx, id, b.ID, RestoreOptions{Database: true, Confirm: "shop"}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !slices.Equal(copied, []string{"restore envoryx-shop-database:TARBYTES", "restore envoryx-shop-db-old:TARBYTES"}) {
		t.Fatalf("restored: %v", copied)
	}
	if c, _ := e.engine.Container("envoryx-shop-database"); c.State != "running" {
		t.Fatal("the database was not started again after the restore")
	}

	// Files a version cannot open are not put back.
	p, _ := e.m.loadProject(ctx, id)
	svc := *p.Service(store.ServiceDatabase)
	err = e.m.restoreDatabaseVolume(ctx, p, &svc, VolumeCopyMeta{Type: "mongodb", Version: "7"}, "unused")
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "copy of mongodb 7 data, which mongodb 8.2 cannot open") {
		t.Fatalf("7.0 copy into 8.2: %v", err)
	}

	// A snapshot needs the server running and says why it cannot.
	_, err = e.m.CreateSnapshot(ctx, id, "old", "")
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "cannot start MongoDB 7.0") {
		t.Fatalf("snapshot of 7.0 on 6.19: %v", err)
	}
}

package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// dumpAndImport makes the fake engine answer the database client: a dump prints one
// statement naming the container it came from, an import records what it was fed.
func dumpAndImport(e *env, imported *[]string) {
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			return "-- dump of " + container + "\n", 0, nil
		case "mariadb":
			*imported = append(*imported, container+": "+strings.TrimSpace(string(stdin)))
			return "", 0, nil
		}
		return "", 1, nil
	}
}

func TestSnapshotDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	dumpAndImport(e, &imported)

	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	snapshot, err := e.m.CreateSnapshot(ctx, id, "", "before the orders migration")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Kind != "database" || snapshot.Meta.Source != "snapshot" || snapshot.Meta.Database == nil || snapshot.Meta.Files != nil {
		t.Fatalf("a snapshot holds the database and nothing else: %+v", snapshot)
	}
	if snapshot.Meta.Note != "before the orders migration" {
		t.Fatalf("note: %q", snapshot.Meta.Note)
	}
	// A full backup is not a snapshot, a database-only backup is listed as one.
	full, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Files: true})
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.m.ListSnapshots(ctx, id, "")
	if err != nil || len(list) != 1 || list[0].ID != snapshot.ID {
		t.Fatalf("snapshots: %+v %v (full backup %s must not be one)", list, err, full.ID)
	}

	// Restoring needs the project's identifier and puts the dump back.
	if _, err := e.m.RestoreSnapshot(ctx, id, "", snapshot.ID, "nope"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("restore without confirmation: %v", err)
	}
	if _, err := e.m.RestoreSnapshot(ctx, id, "", full.ID, "shop"); err != nil {
		t.Fatalf("restoring a database-only part of a full backup is fine: %v", err)
	}
	imported = nil
	if _, err := e.m.RestoreSnapshot(ctx, id, "", snapshot.ID, "shop"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(imported) != 1 || !strings.Contains(imported[0], "envoryx-shop-database: -- dump of envoryx-shop-database") {
		t.Fatalf("import: %v", imported)
	}
}

// A snapshot does not need the project: the database container is started for the dump and
// left as it was found.
func TestSnapshotStoppedProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	dumpAndImport(e, &imported)
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	snapshot, err := e.m.CreateSnapshot(ctx, id, "", "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if db, _ := e.engine.Container("envoryx-shop-database"); db.State == "running" {
		t.Fatal("the database container must be stopped again after the snapshot")
	}
	if _, err := e.m.RestoreSnapshot(ctx, id, "", snapshot.ID, "shop"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if db, _ := e.engine.Container("envoryx-shop-database"); db.State == "running" {
		t.Fatal("the database container must be stopped again after the restore")
	}
	if len(imported) != 1 {
		t.Fatalf("import: %v", imported)
	}
}

func TestSnapshotsRollOver(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	dumpAndImport(e, &imported)
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	// One backup made by hand must survive the roll-over of the snapshots.
	manual, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Note: "keep me"})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for i := range snapshotKeep + 2 {
		s, err := e.m.CreateSnapshot(ctx, id, "", fmt.Sprintf("snapshot %d", i))
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		if i == 0 {
			first = s.ID
		}
	}
	list, err := e.m.ListBackups(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, kept := 0, false
	for _, b := range list {
		if b.Meta.Source == snapshotSource {
			snapshots++
		}
		if b.ID == manual.ID {
			kept = true
		}
		if b.ID == first {
			t.Fatal("the oldest snapshot must be gone")
		}
	}
	if snapshots != snapshotKeep {
		t.Fatalf("kept %d snapshots, want %d", snapshots, snapshotKeep)
	}
	if !kept {
		t.Fatal("a backup made by hand must never be pruned")
	}
}

func TestCloneDatabaseBetweenProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	dumpAndImport(e, &imported)

	staging, err := e.m.Create(ctx, dbRequest("Staging", false))
	if err != nil {
		t.Fatal(err)
	}
	local, err := e.m.Create(ctx, dbRequest("Local", false))
	if err != nil {
		t.Fatal(err)
	}
	// Neither project has to run for a clone.
	for _, p := range []string{staging.Project.ID, local.Project.ID} {
		if _, err := e.m.Stop(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := e.m.CloneDatabase(ctx, local.Project.ID, CloneDatabaseRequest{Source: staging.Project.ID, Snapshot: true, Confirm: "wrong"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("clone without confirmation: %v", err)
	}
	if _, err := e.m.CloneDatabase(ctx, local.Project.ID, CloneDatabaseRequest{Source: local.Project.ID, Confirm: "local"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("cloning a project into itself: %v", err)
	}

	res, err := e.m.CloneDatabase(ctx, local.Project.ID, CloneDatabaseRequest{Source: staging.Project.ID, Snapshot: true, Confirm: "local"})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if res.Source != "staging" || res.Database != "local" {
		t.Fatalf("result: %+v", res)
	}
	// The target was snapshotted first, and the snapshot says what it was taken for.
	if res.Snapshot == nil || res.Snapshot.Kind != "database" || !strings.Contains(res.Snapshot.Meta.Note, "staging") {
		t.Fatalf("snapshot: %+v", res.Snapshot)
	}
	if snapshots, err := e.m.ListSnapshots(ctx, local.Project.ID, ""); err != nil || len(snapshots) != 1 {
		t.Fatalf("the snapshot belongs to the target: %+v %v", snapshots, err)
	}
	if snapshots, _ := e.m.ListSnapshots(ctx, staging.Project.ID, ""); len(snapshots) != 0 {
		t.Fatalf("the source is only read: %+v", snapshots)
	}
	// Staging's dump went into local's database, not the other way round.
	if len(imported) != 1 || imported[0] != "envoryx-local-database: -- dump of envoryx-staging-database" {
		t.Fatalf("import: %v", imported)
	}
	// Both database containers were started for the transfer and stopped again.
	for _, name := range []string{"envoryx-staging-database", "envoryx-local-database"} {
		if c, _ := e.engine.Container(name); c.State == "running" {
			t.Fatalf("%s must be stopped again", name)
		}
	}
}

func TestCloneDatabaseRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	dumpAndImport(e, &imported)

	withDB, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := e.m.Create(ctx, phpRequest("Blog", false))
	if err != nil {
		t.Fatal(err)
	}
	postgres := dbRequest("Reports", false)
	postgres.Database = &DatabaseRequest{Type: "postgresql", Version: "18"}
	pg, err := e.m.Create(ctx, postgres)
	if err != nil {
		t.Fatal(err)
	}

	// A target without a database cannot take one.
	if _, err := e.m.CloneDatabase(ctx, plain.Project.ID, CloneDatabaseRequest{Source: withDB.Project.ID, Confirm: "blog"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("target without a database: %v", err)
	}
	// A source without one has nothing to give.
	if _, err := e.m.CloneDatabase(ctx, withDB.Project.ID, CloneDatabaseRequest{Source: plain.Project.ID, Confirm: "shop"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("source without a database: %v", err)
	}
	// Different flavours: one's dump is not the other's.
	_, err = e.m.CloneDatabase(ctx, withDB.Project.ID, CloneDatabaseRequest{Source: pg.Project.ID, Confirm: "shop"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "postgresql") {
		t.Fatalf("mixed flavours: %v", err)
	}
	if len(imported) != 0 {
		t.Fatalf("nothing may be imported by a refused clone: %v", imported)
	}
}

package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

// Resources with Envoryx labels but no project: containers and networks go on the second
// sighting, volumes stay until the user removes them.
func TestReconcileRemovesOrphanedContainersAndNetworks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A project that exists in Docker only – as after restoring an older database.
	view, err := e.m.Create(ctx, phpRequest("Ghost", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Projects.Delete(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.CreateVolume(ctx, "envoryx-ghost-database", docker.ManagedLabels(view.Project.ID, "ghost", "database", "test")); err != nil {
		t.Fatal(err)
	}

	// First sighting: reported, nothing removed.
	before := len(e.engine.Calls)
	report := e.m.Reconcile(ctx)
	if n := len(report.Orphans); n < 3 {
		t.Fatalf("expected containers, network and volume as orphans, got %+v", report.Orphans)
	}
	if calls := strings.Join(e.engine.Calls[before:], " "); strings.Contains(calls, "remove") || strings.Contains(calls, "stop:") {
		t.Fatalf("first sighting must not remove anything: %s", calls)
	}

	// Second sighting: containers stopped and removed, network removed, volume kept.
	before = len(e.engine.Calls)
	report = e.m.Reconcile(ctx)
	calls := strings.Join(e.engine.Calls[before:], " ")
	if !strings.Contains(calls, "stop:envoryx-ghost-php") || !strings.Contains(calls, "remove:envoryx-ghost-php") {
		t.Fatalf("expected the orphaned containers to be stopped and removed: %s", calls)
	}
	if len(report.Orphans) != 1 || report.Orphans[0].Type != "volume" || report.Orphans[0].Name != "envoryx-ghost-database" {
		t.Fatalf("only the volume may remain: %+v", report.Orphans)
	}
	if act := e.m.Activity(); len(act) != 1 || act[0].Kind != ActivityOrphansRemoved || len(act[0].Items) != 3 {
		t.Fatalf("activity = %+v", act)
	}
	if containers, _ := e.engine.ListContainers(ctx, true, ""); len(containers) != 0 {
		t.Fatalf("containers left: %+v", containers)
	}
	if networks, _ := e.engine.ListNetworks(ctx, true); len(networks) != 0 {
		t.Fatalf("networks left: %+v", networks)
	}

	// The volume stays across passes and goes only on request.
	report = e.m.Reconcile(ctx)
	if len(report.Orphans) != 1 {
		t.Fatalf("volume must never be removed automatically: %+v", report.Orphans)
	}
	if _, err := e.m.RemoveOrphan(ctx, "volume", "no-such-volume"); !errors.Is(err, ErrNotOrphan) {
		t.Fatalf("removing a non-orphan must fail, got %v", err)
	}
	report, err = e.m.RemoveOrphan(ctx, "volume", report.Orphans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Orphans) != 0 {
		t.Fatalf("orphans after removing the volume: %+v", report.Orphans)
	}
	if volumes, _ := e.engine.ListVolumes(ctx, true); len(volumes) != 0 {
		t.Fatalf("volume left: %+v", volumes)
	}
}

// A network another container still uses is reported but left alone.
func TestOrphanedNetworkInForeignUseIsKept(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.AddNetwork("envoryx-ghost", docker.ManagedLabels("ghost-id", "ghost", "", "test"))
	foreignID := e.engine.AddForeignContainer("nextcloud", "nextcloud", "running")
	if err := e.engine.ConnectNetwork(ctx, "envoryx-ghost", foreignID); err != nil {
		t.Fatal(err)
	}

	e.m.Reconcile(ctx)
	report := e.m.Reconcile(ctx)
	if len(report.Orphans) != 1 || report.Orphans[0].Name != "envoryx-ghost" {
		t.Fatalf("network in foreign use must stay listed: %+v", report.Orphans)
	}
	if networks, _ := e.engine.ListNetworks(ctx, true); len(networks) != 1 {
		t.Fatal("network in foreign use must not be removed")
	}
}

package project

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

// testInstance is the instance ID newEnv gives the fake engine.
const testInstance = "test-instance"

// otherLabels are the labels another Envoryx instance on the same host puts on a resource
// of one of its projects.
func otherLabels(projectID, slug, service string) map[string]string {
	l := docker.ManagedLabels(projectID, slug, service, "test")
	l[docker.LabelInstance] = "other-instance"
	return l
}

// Everything Envoryx creates carries its instance ID, and the label is not part of the
// spec fingerprint: containers from before instance labels are not reported as outdated.
func TestCreatedResourcesCarryTheInstanceLabel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Redis = &ExtraRequest{} // for a volume
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	containers, _ := e.engine.ListContainers(ctx, true, v.Project.ID)
	if len(containers) == 0 {
		t.Fatal("no containers")
	}
	for _, c := range containers {
		if c.Labels[docker.LabelInstance] != testInstance {
			t.Fatalf("container %s labels %v", c.Name, c.Labels)
		}
	}
	networks, _ := e.engine.ListNetworks(ctx, true)
	volumes, _ := e.engine.ListVolumes(ctx, true)
	if len(networks) == 0 || len(volumes) == 0 {
		t.Fatalf("networks %v volumes %v", networks, volumes)
	}
	for _, n := range networks {
		if n.Labels[docker.LabelInstance] != testInstance {
			t.Fatalf("network %s labels %v", n.Name, n.Labels)
		}
	}
	for _, vol := range volumes {
		if vol.Labels[docker.LabelInstance] != testInstance {
			t.Fatalf("volume %s labels %v", vol.Name, vol.Labels)
		}
	}
	// The containers of an older Envoryx have no instance label but the same
	// fingerprint: they are adopted as they are, no restart wave.
	for _, c := range containers {
		e.engine.SetLabel(c.Name, docker.LabelInstance, "")
	}
	report := e.m.Reconcile(ctx)
	for _, is := range report.Issues {
		if strings.Contains(is.Message, "older setup") {
			t.Fatalf("an unlabelled container must not count as outdated: %+v", report.Issues)
		}
	}
	if len(report.Orphans) != 0 {
		t.Fatalf("unlabelled containers of a known project are this instance's: %+v", report.Orphans)
	}
	if st := report.States[v.Project.ID]; st.State != StateRunning {
		t.Fatalf("state %s", st.State)
	}
}

// Resources another instance labelled are invisible: not orphans, not stopped at
// shutdown, not removed, and their names cannot be taken over.
func TestOtherInstancesResourcesAreLeftAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.AddImage("php:8.4-fpm")
	e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx-blog-php", Image: "php:8.4-fpm", Labels: otherLabels("blog-id", "blog", "php")}, "running")
	e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx-dbtool", Image: "php:8.4-fpm",
		Labels: map[string]string{docker.LabelManaged: "true", docker.LabelSystem: "dbtool", docker.LabelInstance: "other-instance"}}, "running")
	e.engine.AddNetworkWithSubnet("envoryx-blog", otherLabels("blog-id", "blog", ""), "10.213.0.0/24")
	e.engine.AddVolume("envoryx-blog-database", otherLabels("blog-id", "blog", "database"))

	for i := 0; i < 3; i++ {
		if report := e.m.Reconcile(ctx); len(report.Orphans) != 0 {
			t.Fatalf("pass %d: another instance's resources are no orphans: %+v", i, report.Orphans)
		}
	}
	e.m.StopAllForShutdown(ctx)
	for _, call := range e.engine.Calls {
		if strings.Contains(call, "blog") || strings.Contains(call, "dbtool") {
			t.Fatalf("another instance's resource was touched: %v", e.engine.Calls)
		}
	}
	if c, ok := e.engine.Container("envoryx-blog-php"); !ok || c.State != "running" {
		t.Fatalf("container gone or stopped: %+v", c)
	}
	if err := e.engine.RemoveContainer(ctx, "envoryx-blog-php"); !errors.Is(err, docker.ErrNotManaged) {
		t.Fatalf("removing another instance's container: %v", err)
	}

	// The network pool still steps around the other instance's subnet.
	e.m.cfg.NetworkPool = netip.MustParsePrefix("10.213.0.0/22")
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.engine.NetworkSubnets(NetworkName(v.Project.Slug)); len(got) != 1 || got[0] != "10.213.1.0/24" {
		t.Fatalf("subnet %v", got)
	}

	// A project of the same name would share the other one's network and data.
	_, err = e.m.Create(ctx, phpRequest("Blog", false))
	if !errors.Is(err, docker.ErrOtherInstance) {
		t.Fatalf("same name as another instance's project: %v", err)
	}
	if _, ok := e.engine.Container("envoryx-blog-php"); !ok {
		t.Fatal("the rollback removed the other instance's container")
	}
}

// Unlabelled resources of unknown projects may be an older instance's next door: they are
// reported, never removed automatically, and go only on request.
func TestUnlabelledOrphansAreReportedButKept(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.AddImage("php:8.4-fpm")
	e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx-old-php", Image: "php:8.4-fpm", Labels: docker.ManagedLabels("old-id", "old", "php", "0.17.0")}, "running")
	e.engine.AddNetwork("envoryx-old", docker.ManagedLabels("old-id", "old", "", "0.17.0"))

	var report ReconcileReport
	for i := 0; i < 3; i++ {
		report = e.m.Reconcile(ctx)
	}
	if len(report.Orphans) != 2 {
		t.Fatalf("orphans %+v", report.Orphans)
	}
	for _, o := range report.Orphans {
		if !o.Unlabelled {
			t.Fatalf("not marked unlabelled: %+v", o)
		}
	}
	e.m.StopAllForShutdown(ctx)
	for _, call := range e.engine.Calls {
		if strings.Contains(call, "envoryx-old") {
			t.Fatalf("an unlabelled orphan was touched: %v", e.engine.Calls)
		}
	}
	if len(e.m.Activity()) != 0 {
		t.Fatalf("activity %+v", e.m.Activity())
	}

	for _, o := range report.Orphans {
		if o.Type == "container" {
			if report, err := e.m.RemoveOrphan(ctx, o.Type, o.ID); err != nil || len(report.Orphans) != 1 {
				t.Fatalf("manual removal: %v %+v", err, report.Orphans)
			}
		}
	}
	if _, ok := e.engine.Container("envoryx-old-php"); ok {
		t.Fatal("the container must go on request")
	}
}

// The stats summary counts only this instance's containers.
func TestOwnContainersDropsUnclaimed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	legacy := docker.ManagedLabels(v.Project.ID, "shop", "php", "0.17.0")
	system := map[string]string{docker.LabelManaged: "true", docker.LabelSystem: "dbtool"}
	unknown := docker.ManagedLabels("gone", "gone", "php", "0.17.0")
	own := maps.Clone(unknown)
	own[docker.LabelInstance] = testInstance
	in := []docker.Container{{Name: "legacy", Labels: legacy}, {Name: "system", Labels: system}, {Name: "unknown", Labels: unknown}, {Name: "own-orphan", Labels: own}}
	var names []string
	for _, c := range e.m.OwnContainers(ctx, in) {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "legacy,system,own-orphan" {
		t.Fatalf("kept %v", names)
	}
}

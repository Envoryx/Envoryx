package stats

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
)

func addContainer(f *dockertest.Fake, project, service, state string) {
	f.AddManagedContainer(docker.ContainerSpec{Name: project + "-" + service, Image: "img", Labels: docker.ManagedLabels(project, project, service, "test")}, state)
}

func newCollector(f *dockertest.Fake, ttl time.Duration) *Collector {
	return New(f, ttl, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestProjectSamplesOnlyItsContainers(t *testing.T) {
	f := dockertest.New()
	addContainer(f, "p1", "php", "running")
	addContainer(f, "p1", "mysql", "running")
	addContainer(f, "p1", "worker", "exited")
	addContainer(f, "p2", "php", "running")
	addContainer(f, "p2", "redis", "running")
	f.AddForeignContainer("other", "img", "running")
	f.StatsByName = map[string]docker.Stats{"p1-php": {CPUPercent: 10, MemoryBytes: 100}, "p1-mysql": {CPUPercent: 5, MemoryBytes: 50}}
	c := newCollector(f, time.Minute)

	p, err := c.Project(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Containers != 3 || p.Running != 2 || p.CPUPercent != 15 || p.MemoryBytes != 150 || len(p.PerContainer) != 2 || p.SampledAt.IsZero() {
		t.Fatalf("usage: %+v", p)
	}
	for _, pc := range p.PerContainer {
		if pc.ProjectID != "p1" {
			t.Fatalf("sample of another project: %+v", pc)
		}
	}
	// Only p1's running containers were read, and without the inspecting ContainerStats.
	if guarded, listed := f.StatsCalls(); guarded != 0 || listed != 2 {
		t.Fatalf("stats calls: guarded %d, listed %d", guarded, listed)
	}

	// Cached per project: a second read samples nothing, another project samples its own.
	if _, err := c.Project(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, listed := f.StatsCalls(); listed != 2 {
		t.Fatalf("p1 not cached: %d calls", listed)
	}
	p2, err := c.Project(context.Background(), "p2")
	if err != nil || p2.Running != 2 {
		t.Fatalf("p2: %+v %v", p2, err)
	}
	if _, listed := f.StatsCalls(); listed != 4 {
		t.Fatalf("p2 sampled %d containers", listed-2)
	}
}

func TestProjectCacheExpires(t *testing.T) {
	f := dockertest.New()
	addContainer(f, "p1", "php", "running")
	c := newCollector(f, 20*time.Millisecond)
	if _, err := c.Project(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := c.Project(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, listed := f.StatsCalls(); listed != 2 {
		t.Fatalf("expired cache not refreshed: %d calls", listed)
	}
}

func TestSummaryCoversAllProjects(t *testing.T) {
	f := dockertest.New()
	addContainer(f, "p1", "php", "running")
	addContainer(f, "p1", "mysql", "exited")
	addContainer(f, "p2", "php", "running")
	c := newCollector(f, time.Minute)
	s, err := c.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Containers != 3 || s.Running != 2 || s.CPUPercent != 3 || len(s.PerProject) != 2 {
		t.Fatalf("summary: %+v", s)
	}
	if p1 := s.PerProject["p1"]; p1.Containers != 2 || p1.Running != 1 || len(p1.PerContainer) != 1 {
		t.Fatalf("p1: %+v", p1)
	}
	if guarded, listed := f.StatsCalls(); guarded != 0 || listed != 2 {
		t.Fatalf("stats calls: guarded %d, listed %d", guarded, listed)
	}
}

func TestListedContainerStatsRefusesUnmanaged(t *testing.T) {
	f := dockertest.New()
	id := f.AddForeignContainer("other", "img", "running")
	if _, err := f.ListedContainerStats(context.Background(), docker.Container{ID: id, Labels: map[string]string{}}); err == nil {
		t.Fatal("unmanaged container sampled")
	}
}

// The summary counts this instance's containers only: another instance's are not listed
// as managed, and Filter drops what the project code cannot claim.
func TestSummaryCountsOwnContainers(t *testing.T) {
	f := dockertest.New()
	f.Instance = "a"
	addContainer(f, "p1", "php", "running")
	f.AddManagedContainer(docker.ContainerSpec{Name: "other-php", Image: "img",
		Labels: docker.StampInstance("b", docker.ManagedLabels("p9", "p9", "php", "test"))}, "running")
	addContainer(f, "gone", "php", "running")
	c := newCollector(f, time.Minute)
	c.Filter = func(_ context.Context, cs []docker.Container) []docker.Container {
		var out []docker.Container
		for _, ct := range cs {
			if ct.ProjectID() != "gone" {
				out = append(out, ct)
			}
		}
		return out
	}
	s, err := c.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Containers != 1 || s.Running != 1 || len(s.PerProject) != 1 {
		t.Fatalf("summary: %+v", s)
	}
}

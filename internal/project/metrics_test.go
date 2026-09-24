package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestResourceHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "mariadb"}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "php:script", Arg: "worker.php", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	other, err := e.m.Create(ctx, phpRequest("Blog", true))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "big.bin"), make([]byte, 5000), 0o644)
	e.engine.VolumeBytes = map[string]int64{VolumeName("shop", store.ServiceDatabase): 123456}

	stats := func(cpu float64, rx uint64, written uint64) {
		e.engine.StatsByName = map[string]docker.Stats{
			"envoryx-shop-php":      {CPUPercent: cpu, MemoryBytes: 100 << 20, NetRxBytes: rx, NetTxBytes: rx / 2, BlockWritten: written},
			"envoryx-shop-database": {CPUPercent: 10, MemoryBytes: 300 << 20},
		}
	}
	t0 := time.Now().Truncate(time.Minute).Add(-2 * time.Minute)
	stats(50, 1000, 0)
	e.m.MetricsPass(ctx, t0)
	stats(150, 1000+60*2048, 60*4096)
	e.m.MetricsPass(ctx, t0.Add(time.Minute))

	pm, err := e.m.ProjectMetrics(ctx, id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Res != store.MetricRes1m {
		t.Fatalf("resolution: %d", pm.Res)
	}
	var php, worker *ContainerSeries
	for i := range pm.Containers {
		switch pm.Containers[i].Name {
		case "php":
			php = &pm.Containers[i]
		case "worker queue":
			worker = &pm.Containers[i]
		}
	}
	if php == nil || worker == nil || php.Group != "app" || worker.Group != "app" {
		t.Fatalf("containers: %+v", pm.Containers)
	}
	if len(php.Points) != 2 {
		t.Fatalf("php points: %v", php.Points)
	}
	second := php.Points[1]
	// [ts, cpu, cpuMax, mem, memMax, rx, tx, read, write]
	if second[1] != 150 || second[3] != float64(100<<20) || second[5] != 2048 || second[6] != 1024 || second[8] != 4096 {
		t.Fatalf("second php sample: %v", second)
	}
	if php.Points[0][5] != 0 {
		t.Fatalf("the first sample has no previous counters, so no rate: %v", php.Points[0])
	}

	// Rolled up: one five-minute (or two, across a boundary) and one hourly bucket with the
	// average of the minutes.
	fives, _ := e.store.Metrics.Samples(ctx, id, store.MetricRes5m, 0, time.Now().Unix()+3600)
	var cpu float64
	var n int
	for _, s := range fives {
		if s.Container == "php" {
			cpu += s.CPU * float64(s.Samples)
			n += s.Samples
		}
	}
	if n != 2 || cpu/float64(n) != 100 {
		t.Fatalf("five-minute rollup: %+v", fives)
	}

	// Sizes: the volume, the directory, the backups.
	kinds := map[string]int64{}
	for _, s := range pm.Sizes {
		kinds[s.Kind] = s.Points[len(s.Points)-1][1]
	}
	if kinds["volume"] != 123456 || kinds["files"] < 5000 || kinds["backups"] != 0 {
		t.Fatalf("sizes: %+v", pm.Sizes)
	}

	ov, err := e.m.MetricsOverview(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// php 150 + database 10 + web and worker 1.5 each
	if len(ov.Projects) != 2 || ov.Projects[0].ID != id || ov.Projects[0].CPUNow != 163 || ov.Projects[0].Disk["volume"] != 123456 {
		t.Fatalf("overview: %+v", ov.Projects)
	}
	if ov.Projects[0].NetRx != 2048*60 {
		t.Fatalf("bytes received over the range: %d", ov.Projects[0].NetRx)
	}
	if ov.Projects[1].ID != other.Project.ID {
		t.Fatalf("order: %+v", ov.Projects)
	}

	// Deleting a project deletes its history.
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := e.store.Metrics.Samples(ctx, id, store.MetricRes1m, 0, time.Now().Unix()+60); len(rows) != 0 {
		t.Fatalf("history of a deleted project: %d rows", len(rows))
	}
}

func TestResourceHistoryRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if got := e.m.Metrics(ctx).RetentionDays; got != DefaultMetricsDays {
		t.Fatalf("default: %d", got)
	}
	if _, err := e.m.SetMetricsRetention(ctx, 0); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("0 days: %v", err)
	}
	if _, err := e.m.SetMetricsRetention(ctx, 7); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	_ = e.store.Metrics.AddSamples(ctx, []store.MetricSample{
		{ProjectID: "p", Container: "php", Res: store.MetricRes1h, TS: old.Unix(), Samples: 1},
		{ProjectID: "p", Container: "php", Res: store.MetricRes1h, TS: time.Now().Add(-time.Hour).Unix(), Samples: 1},
	})
	e.m.MetricsPass(ctx, time.Now())
	rows, _ := e.store.Metrics.Samples(ctx, "p", store.MetricRes1h, 0, time.Now().Unix()+3600)
	if len(rows) != 1 {
		t.Fatalf("after pruning to 7 days: %+v", rows)
	}
}

func TestMetricsResolution(t *testing.T) {
	for rng, want := range map[time.Duration]int{time.Hour: 60, 6 * time.Hour: 60, 24 * time.Hour: 300, 7 * 24 * time.Hour: 3600, 90 * 24 * time.Hour: 3600} {
		if got := MetricsResolution(rng); got != want {
			t.Errorf("%v: %d, want %d", rng, got, want)
		}
	}
}

// A project created after the hour's measurement is measured at the next pass, not an
// hour later; one already measured this hour is not walked again.
func TestSizesForNewProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now()
	e.m.MetricsPass(ctx, now) // no project yet
	v, err := e.m.Create(ctx, phpRequest("Late", false))
	if err != nil {
		t.Fatal(err)
	}
	e.m.MetricsPass(ctx, now.Add(time.Minute))
	sizes, _ := e.store.Metrics.Sizes(ctx, v.Project.ID, 0, now.Unix()+7200)
	if len(sizes) == 0 {
		t.Fatal("a new project must be measured at the next pass")
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "late", "more.bin"), make([]byte, 10000), 0o644)
	e.m.MetricsPass(ctx, now.Add(2*time.Minute))
	again, _ := e.store.Metrics.Sizes(ctx, v.Project.ID, 0, now.Unix()+7200)
	if len(again) != len(sizes) || again[len(again)-1].Bytes != sizes[len(sizes)-1].Bytes {
		t.Fatalf("measured twice in one hour: %+v → %+v", sizes, again)
	}
}

package project

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// The resource history samples every running project container once a minute (CPU,
// memory, network and block I/O) and measures each project's disk space once an hour
// (its volumes, its directory, its backups). Minute samples are rolled up into
// five-minute and hourly buckets as they come; minutes are kept for a day, five-minute
// buckets for a week and hours (and the sizes) for the configured retention.

// Settings key and bounds of the retention.
const (
	SettingMetricsDays = "metrics_retention_days"
	DefaultMetricsDays = 90
	MaxMetricsDays     = 365
	keepMinutes        = 24 * time.Hour
	keepFiveMinutes    = 7 * 24 * time.Hour
)

// counters are the cumulative values of one container at one moment.
type counters struct {
	at                    time.Time
	rx, tx, read, written uint64
}

type metricsState struct {
	mu        sync.Mutex
	last      map[string]counters // by container id
	lastPrune time.Time
}

// MetricsSettings is the stored configuration of the resource history.
type MetricsSettings struct {
	RetentionDays int `json:"retentionDays"`
}

// MetricsInfo is what the settings page shows.
type MetricsInfo struct {
	MetricsSettings
	Samples int64 `json:"samples"`
	Sizes   int64 `json:"sizes"`
}

// Metrics returns the retention.
func (m *Manager) Metrics(ctx context.Context) MetricsSettings {
	return MetricsSettings{RetentionDays: m.intSetting(ctx, SettingMetricsDays, DefaultMetricsDays, 1, MaxMetricsDays)}
}

// MetricsInfo returns the retention and how much the history holds.
func (m *Manager) MetricsInfo(ctx context.Context) MetricsInfo {
	info := MetricsInfo{MetricsSettings: m.Metrics(ctx)}
	info.Samples, info.Sizes, _ = m.store.Metrics.Usage(ctx)
	return info
}

// SetMetricsRetention stores the retention; older data goes at the next hourly pass.
func (m *Manager) SetMetricsRetention(ctx context.Context, days int) (MetricsSettings, error) {
	if days < 1 || days > MaxMetricsDays {
		return MetricsSettings{}, fmt.Errorf("%w: keep the resource history for 1 to %d days", validate.ErrInvalid, MaxMetricsDays)
	}
	if err := m.store.Settings.Set(ctx, SettingMetricsDays, strconv.Itoa(days)); err != nil {
		return MetricsSettings{}, err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"metricsRetentionDays": days})
	st := m.metricsState()
	st.mu.Lock()
	st.lastPrune = time.Time{}
	st.mu.Unlock()
	return m.Metrics(ctx), nil
}

// ClearMetrics deletes the whole resource history.
func (m *Manager) ClearMetrics(ctx context.Context) error {
	if err := m.store.Metrics.Clear(ctx); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"metricsCleared": true})
	return nil
}

func (m *Manager) metricsState() *metricsState {
	m.metricsOnce.Do(func() { m.metrics = &metricsState{last: map[string]counters{}} })
	return m.metrics
}

// RunMetrics samples every interval until ctx ends.
func (m *Manager) RunMetrics(ctx context.Context, interval time.Duration, log *slog.Logger) {
	for {
		m.MetricsPass(ctx, time.Now())
		// Aligned to the interval, so every minute gets exactly one sample.
		wait := time.Until(time.Now().Truncate(interval).Add(interval))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// MetricsPass samples, rolls up, measures the sizes when an hour has passed and prunes.
func (m *Manager) MetricsPass(ctx context.Context, now time.Time) {
	if err := m.sampleMetrics(ctx, now); err != nil {
		m.log.Debug("resource history: sampling failed", "err", err)
	}
	nowUnix := now.Unix()
	// Recompute the recent buckets, the one in progress included, so the charts show the
	// current five minutes and hour as well.
	if err := m.store.Metrics.Rollup(ctx, store.MetricRes1m, store.MetricRes5m, bucket(nowUnix-2*store.MetricRes5m, store.MetricRes5m), nowUnix+store.MetricRes5m); err != nil {
		m.log.Warn("resource history", "err", err)
	}
	if err := m.store.Metrics.Rollup(ctx, store.MetricRes5m, store.MetricRes1h, bucket(nowUnix-2*store.MetricRes1h, store.MetricRes1h), nowUnix+store.MetricRes1h); err != nil {
		m.log.Warn("resource history", "err", err)
	}
	st := m.metricsState()
	st.mu.Lock()
	prune := now.Sub(st.lastPrune) >= time.Hour
	if prune {
		st.lastPrune = now
	}
	st.mu.Unlock()
	// Disk space once per clock hour and project: a project without a measurement in
	// this hour (the hour just began, or the project is new) is measured now.
	if err := m.measureSizes(ctx, now); err != nil {
		m.log.Warn("resource history: measuring disk space failed", "err", err)
	}
	if prune {
		keep := time.Duration(m.Metrics(ctx).RetentionDays) * 24 * time.Hour
		five := min(keepFiveMinutes, keep)
		_ = m.store.Metrics.Prune(ctx, store.MetricRes1m, now.Add(-min(keepMinutes, keep)).Unix())
		_ = m.store.Metrics.Prune(ctx, store.MetricRes5m, now.Add(-five).Unix())
		_ = m.store.Metrics.Prune(ctx, store.MetricRes1h, now.Add(-keep).Unix())
		_ = m.store.Metrics.PruneSizes(ctx, now.Add(-keep).Unix())
	}
}

func bucket(ts int64, res int) int64 { return ts / int64(res) * int64(res) }

// metricName names a container in the history: its service, or "worker <name>".
func metricName(slug string, c docker.Container) string {
	svc := c.Service()
	if strings.HasPrefix(svc, "worker:") {
		if name, ok := strings.CutPrefix(c.Name, "envoryx-"+slug+"-worker-"); ok {
			return "worker " + name
		}
	}
	return svc
}

// rate is the per-second change of a counter; a smaller value (the container restarted)
// counts as no traffic rather than a negative one.
func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

func (m *Manager) sampleMetrics(ctx context.Context, now time.Time) error {
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		return err
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return err
	}
	slugs := map[string]string{}
	for _, p := range projects {
		slugs[p.ID] = p.Slug
	}
	type result struct {
		c  docker.Container
		st docker.Stats
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []result
		sem     = make(chan struct{}, 8)
	)
	for _, c := range containers {
		if c.State != "running" || slugs[c.ProjectID()] == "" || c.Service() == "" {
			continue
		}
		wg.Add(1)
		go func(c docker.Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			st, err := m.engine.ContainerStats(sctx, c.ID)
			if err != nil {
				return
			}
			mu.Lock()
			results = append(results, result{c, st})
			mu.Unlock()
		}(c)
	}
	wg.Wait()

	state := m.metricsState()
	state.mu.Lock()
	defer state.mu.Unlock()
	ts := bucket(now.Unix(), store.MetricRes1m)
	seen := map[string]bool{}
	var samples []store.MetricSample
	for _, r := range results {
		seen[r.c.ID] = true
		cur := counters{at: now, rx: r.st.NetRxBytes, tx: r.st.NetTxBytes, read: r.st.BlockRead, written: r.st.BlockWritten}
		prev, had := state.last[r.c.ID]
		state.last[r.c.ID] = cur
		s := store.MetricSample{ProjectID: r.c.ProjectID(), Container: metricName(slugs[r.c.ProjectID()], r.c), Res: store.MetricRes1m, TS: ts,
			CPU: r.st.CPUPercent, CPUMax: r.st.CPUPercent, Mem: r.st.MemoryBytes, MemMax: r.st.MemoryBytes, Samples: 1}
		if had {
			dt := now.Sub(prev.at).Seconds()
			s.NetRx, s.NetTx = rate(cur.rx, prev.rx, dt), rate(cur.tx, prev.tx, dt)
			s.BlkRead, s.BlkWrite = rate(cur.read, prev.read, dt), rate(cur.written, prev.written, dt)
		}
		samples = append(samples, s)
	}
	for id := range state.last {
		if !seen[id] {
			delete(state.last, id)
		}
	}
	return m.store.Metrics.AddSamples(ctx, samples)
}

// measureSizes records the disk space of the projects not measured in the current hour
// yet: each volume, the directory and the local backups.
func (m *Manager) measureSizes(ctx context.Context, now time.Time) error {
	ts := bucket(now.Unix(), store.MetricRes1h)
	done, err := m.store.Metrics.SizedProjects(ctx, ts)
	if err != nil {
		return err
	}
	all, err := m.store.Projects.List(ctx)
	if err != nil {
		return err
	}
	var projects []store.Project
	for _, p := range all {
		if !done[p.ID] {
			projects = append(projects, p)
		}
	}
	if len(projects) == 0 {
		return nil
	}
	todo := map[string]bool{}
	for _, p := range projects {
		todo[p.ID] = true
	}
	var sizes []store.MetricSize
	if vols, err := m.engine.VolumeSizes(ctx); err == nil {
		for _, v := range vols {
			if id := v.Labels[docker.LabelProjectID]; todo[id] && v.Bytes >= 0 {
				sizes = append(sizes, store.MetricSize{ProjectID: id, Kind: "volume", Name: v.Name, TS: ts, Bytes: v.Bytes})
			}
		}
	} else {
		m.log.Debug("resource history: volume sizes", "err", err)
	}
	paths, perr := m.paths()
	for _, p := range projects {
		if perr == nil {
			dir := NewPlanner(paths, m.catalog).ProjectDir(p)
			sizes = append(sizes, store.MetricSize{ProjectID: p.ID, Kind: "files", Name: p.Path, TS: ts, Bytes: dirSize(dir)})
		}
		if rows, err := m.store.Backups.ListByProject(ctx, p.ID); err == nil {
			var total int64
			for _, b := range rows {
				total += b.SizeBytes
			}
			sizes = append(sizes, store.MetricSize{ProjectID: p.ID, Kind: "backups", Name: "backups", TS: ts, Bytes: total})
		}
	}
	return m.store.Metrics.AddSizes(ctx, sizes)
}

// ---- queries ----------------------------------------------------------------

// MetricsResolution picks the bucket size for a time range: about 60 to 400 points.
func MetricsResolution(rng time.Duration) int {
	switch {
	case rng <= 6*time.Hour:
		return store.MetricRes1m
	case rng <= 48*time.Hour:
		return store.MetricRes5m
	default:
		return store.MetricRes1h
	}
}

// MetricPoint is one bucket of a container: [ts, cpu, cpuMax, mem, memMax, rx, tx,
// read, write] – compact on the wire.
type MetricPoint [9]float64

// ContainerSeries is one container's history.
type ContainerSeries struct {
	Name   string        `json:"name"`
	Group  string        `json:"group"` // app | services
	Points []MetricPoint `json:"points"`
}

// SizeSeries is one thing's disk space over time: [ts, bytes].
type SizeSeries struct {
	Kind   string     `json:"kind"`
	Name   string     `json:"name"`
	Points [][2]int64 `json:"points"`
}

// ProjectMetrics is a project's resource history over a range.
type ProjectMetrics struct {
	From       int64             `json:"from"`
	To         int64             `json:"to"`
	Res        int               `json:"res"`
	Containers []ContainerSeries `json:"containers"`
	Sizes      []SizeSeries      `json:"sizes"`
}

func groupOfName(name string) string {
	if strings.HasPrefix(name, "worker ") {
		return "app"
	}
	return LimitGroup(store.ServiceKind(name))
}

// ProjectMetrics returns a project's history for the range ending now.
func (m *Manager) ProjectMetrics(ctx context.Context, id string, rng time.Duration) (ProjectMetrics, error) {
	if err := validate.UUID(id); err != nil {
		return ProjectMetrics{}, ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, id); err != nil {
		return ProjectMetrics{}, err
	}
	now := time.Now()
	res := MetricsResolution(rng)
	from, to := bucket(now.Add(-rng).Unix(), res), now.Unix()+1
	out := ProjectMetrics{From: from, To: to, Res: res, Containers: []ContainerSeries{}, Sizes: []SizeSeries{}}
	rows, err := m.store.Metrics.Samples(ctx, id, res, from, to)
	if err != nil {
		return ProjectMetrics{}, err
	}
	byName := map[string]*ContainerSeries{}
	for _, r := range rows {
		s := byName[r.Container]
		if s == nil {
			s = &ContainerSeries{Name: r.Container, Group: groupOfName(r.Container)}
			byName[r.Container] = s
		}
		s.Points = append(s.Points, MetricPoint{float64(r.TS), r.CPU, r.CPUMax, float64(r.Mem), float64(r.MemMax), r.NetRx, r.NetTx, r.BlkRead, r.BlkWrite})
	}
	for _, s := range byName {
		out.Containers = append(out.Containers, *s)
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].Name < out.Containers[j].Name })
	// Sizes are hourly; over a short range the latest measurement before it still counts.
	sizeFrom := min(from, bucket(now.Unix(), store.MetricRes1h)-store.MetricRes1h)
	sizes, err := m.store.Metrics.Sizes(ctx, id, sizeFrom, to)
	if err != nil {
		return ProjectMetrics{}, err
	}
	bySize := map[string]*SizeSeries{}
	for _, r := range sizes {
		key := r.Kind + "\x00" + r.Name
		s := bySize[key]
		if s == nil {
			s = &SizeSeries{Kind: r.Kind, Name: r.Name}
			bySize[key] = s
		}
		s.Points = append(s.Points, [2]int64{r.TS, r.Bytes})
	}
	for _, s := range bySize {
		out.Sizes = append(out.Sizes, *s)
	}
	sort.Slice(out.Sizes, func(i, j int) bool {
		if out.Sizes[i].Kind != out.Sizes[j].Kind {
			return out.Sizes[i].Kind < out.Sizes[j].Kind
		}
		return out.Sizes[i].Name < out.Sizes[j].Name
	})
	return out, nil
}

// ProjectUsage summarises one project over a range for the dashboard.
type ProjectUsage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// CPU in percent of one core, memory in bytes: the project's containers together.
	CPUAvg float64 `json:"cpuAvg"`
	CPUMax float64 `json:"cpuMax"`
	CPUNow float64 `json:"cpuNow"`
	MemAvg int64   `json:"memAvg"`
	MemMax int64   `json:"memMax"`
	MemNow int64   `json:"memNow"`
	// Bytes moved over the range.
	NetRx    int64 `json:"netRx"`
	NetTx    int64 `json:"netTx"`
	BlkRead  int64 `json:"blkRead"`
	BlkWrite int64 `json:"blkWrite"`
	// Disk is the latest measured space by kind (volume, files, backups).
	Disk map[string]int64 `json:"disk"`
	// Series is the project total per bucket: [ts, cpu, mem].
	Series [][3]float64 `json:"series"`
}

// MetricsOverview is every project's usage over a range.
type MetricsOverview struct {
	From     int64          `json:"from"`
	To       int64          `json:"to"`
	Res      int            `json:"res"`
	Projects []ProjectUsage `json:"projects"`
}

// MetricsOverview compares the projects over the range ending now, busiest first.
func (m *Manager) MetricsOverview(ctx context.Context, rng time.Duration) (MetricsOverview, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return MetricsOverview{}, err
	}
	now := time.Now()
	res := MetricsResolution(rng)
	from, to := bucket(now.Add(-rng).Unix(), res), now.Unix()+1
	rows, err := m.store.Metrics.AllSamples(ctx, res, from, to)
	if err != nil {
		return MetricsOverview{}, err
	}
	type agg struct {
		byTS map[int64][2]float64 // cpu, mem summed over the containers
		u    ProjectUsage
	}
	per := map[string]*agg{}
	for _, p := range projects {
		per[p.ID] = &agg{byTS: map[int64][2]float64{}, u: ProjectUsage{ID: p.ID, Name: p.Name, Slug: p.Slug, Disk: map[string]int64{}, Series: [][3]float64{}}}
	}
	for _, r := range rows {
		a := per[r.ProjectID]
		if a == nil {
			continue
		}
		v := a.byTS[r.TS]
		v[0] += r.CPU
		v[1] += float64(r.Mem)
		a.byTS[r.TS] = v
		secs := float64(res)
		a.u.NetRx += int64(r.NetRx * secs)
		a.u.NetTx += int64(r.NetTx * secs)
		a.u.BlkRead += int64(r.BlkRead * secs)
		a.u.BlkWrite += int64(r.BlkWrite * secs)
	}
	sizes, err := m.store.Metrics.Sizes(ctx, "", bucket(now.Unix(), store.MetricRes1h)-2*store.MetricRes1h, to)
	if err != nil {
		return MetricsOverview{}, err
	}
	latest := map[string]int64{}
	for _, s := range sizes {
		if s.TS > latest[s.ProjectID] {
			latest[s.ProjectID] = s.TS
		}
	}
	for _, s := range sizes {
		if a := per[s.ProjectID]; a != nil && s.TS == latest[s.ProjectID] {
			a.u.Disk[s.Kind] += s.Bytes
		}
	}
	out := MetricsOverview{From: from, To: to, Res: res, Projects: []ProjectUsage{}}
	for _, a := range per {
		tss := make([]int64, 0, len(a.byTS))
		for ts := range a.byTS {
			tss = append(tss, ts)
		}
		sort.Slice(tss, func(i, j int) bool { return tss[i] < tss[j] })
		var cpuSum, memSum float64
		for _, ts := range tss {
			v := a.byTS[ts]
			a.u.Series = append(a.u.Series, [3]float64{float64(ts), v[0], v[1]})
			cpuSum += v[0]
			memSum += v[1]
			a.u.CPUMax = max(a.u.CPUMax, v[0])
			a.u.MemMax = max(a.u.MemMax, int64(v[1]))
		}
		if n := len(tss); n > 0 {
			a.u.CPUAvg = cpuSum / float64(n)
			a.u.MemAvg = int64(memSum / float64(n))
			last := a.byTS[tss[n-1]]
			// Only a bucket that is current counts as "now".
			if tss[n-1] >= bucket(now.Unix(), res)-int64(res) {
				a.u.CPUNow, a.u.MemNow = last[0], int64(last[1])
			}
		}
		out.Projects = append(out.Projects, a.u)
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		if out.Projects[i].CPUAvg != out.Projects[j].CPUAvg {
			return out.Projects[i].CPUAvg > out.Projects[j].CPUAvg
		}
		return out.Projects[i].Name < out.Projects[j].Name
	})
	return out, nil
}

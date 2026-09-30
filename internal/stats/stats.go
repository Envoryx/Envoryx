// Package stats samples resource usage of managed containers with a short cache.
package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// ContainerStats is one cached sample.
type ContainerStats struct {
	ContainerID string  `json:"containerId"`
	Name        string  `json:"name"`
	ProjectID   string  `json:"projectId"`
	Service     string  `json:"service"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
	MemoryLimit int64   `json:"memoryLimit"`
}

// Summary aggregates all managed containers.
type Summary struct {
	Containers  int              `json:"containers"`
	Running     int              `json:"running"`
	CPUPercent  float64          `json:"cpuPercent"`
	MemoryBytes int64            `json:"memoryBytes"`
	PerProject  map[string]Usage `json:"perProject"`
	SampledAt   time.Time        `json:"sampledAt"`
}

// Usage is the aggregated usage of one project.
type Usage struct {
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
	Running     int     `json:"running"`
	Containers  int     `json:"containers"`
	// PerContainer are the samples of the running containers.
	PerContainer []ContainerStats `json:"perContainer,omitempty"`
}

// ProjectUsage is the usage of one project with the time it was sampled.
type ProjectUsage struct {
	Usage
	SampledAt time.Time `json:"sampledAt"`
}

// Collector samples stats on demand and caches the result.
type Collector struct {
	engine docker.Engine
	log    *slog.Logger
	ttl    time.Duration
	// Filter, when set, narrows the managed containers the summary counts, e.g. to drop
	// unlabelled ones of projects another Envoryx instance on the host may own.
	Filter func(context.Context, []docker.Container) []docker.Container

	mu      sync.Mutex
	summary cacheEntry[Summary]
	// projects caches Project per project id, so a project page samples only its own
	// containers instead of every managed container on the host.
	projects map[string]*cacheEntry[ProjectUsage]
}

// cacheEntry is one cached sample and the refresh in flight, if any.
type cacheEntry[T any] struct {
	val       T
	err       error
	sampledAt time.Time
	inFly     bool
	waiters   []chan struct{}
}

// New creates a collector with the given cache TTL.
func New(engine docker.Engine, ttl time.Duration, log *slog.Logger) *Collector {
	return &Collector{engine: engine, ttl: ttl, log: log, projects: map[string]*cacheEntry[ProjectUsage]{}}
}

// Summary returns cached stats of all managed containers, refreshing when older than the
// TTL. Concurrent callers share one refresh.
func (c *Collector) Summary(ctx context.Context) (Summary, error) {
	c.mu.Lock()
	return cached(ctx, c, &c.summary, c.collect)
}

// Project returns cached stats of one project's containers, refreshing when older than
// the TTL. Concurrent callers for the same project share one refresh.
func (c *Collector) Project(ctx context.Context, projectID string) (ProjectUsage, error) {
	c.mu.Lock()
	for id, e := range c.projects {
		if !e.inFly && time.Since(e.sampledAt) >= c.ttl {
			delete(c.projects, id) // also drops deleted projects
		}
	}
	e := c.projects[projectID]
	if e == nil {
		e = &cacheEntry[ProjectUsage]{}
		c.projects[projectID] = e
	}
	return cached(ctx, c, e, func(ctx context.Context) (ProjectUsage, error) {
		return c.collectProject(ctx, projectID)
	})
}

// cached returns e's value or refreshes it with fetch. c.mu must be held; it is released.
func cached[T any](ctx context.Context, c *Collector, e *cacheEntry[T], fetch func(context.Context) (T, error)) (T, error) {
	if time.Since(e.sampledAt) < c.ttl {
		v, err := e.val, e.err
		c.mu.Unlock()
		return v, err
	}
	if e.inFly {
		ch := make(chan struct{})
		e.waiters = append(e.waiters, ch)
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
		c.mu.Lock()
		v, err := e.val, e.err
		c.mu.Unlock()
		return v, err
	}
	e.inFly = true
	c.mu.Unlock()

	v, err := fetch(ctx)

	c.mu.Lock()
	e.val, e.err, e.sampledAt = v, err, time.Now()
	e.inFly = false
	for _, w := range e.waiters {
		close(w)
	}
	e.waiters = nil
	c.mu.Unlock()
	return v, err
}

func (c *Collector) collect(ctx context.Context) (Summary, error) {
	s := Summary{PerProject: map[string]Usage{}, SampledAt: time.Now().UTC()}
	containers, err := c.engine.ListContainers(ctx, true, "")
	if err != nil {
		return s, err
	}
	if c.Filter != nil {
		containers = c.Filter(ctx, containers)
	}
	s.Containers = len(containers)
	samples := c.sample(ctx, containers)
	for i, ct := range containers {
		u := s.PerProject[ct.ProjectID()]
		add(&u, ct, samples[i])
		s.PerProject[ct.ProjectID()] = u
		if ct.State == "running" {
			s.Running++
			if samples[i].err == nil {
				s.CPUPercent += samples[i].stats.CPUPercent
				s.MemoryBytes += samples[i].stats.MemoryBytes
			}
		}
	}
	return s, nil
}

func (c *Collector) collectProject(ctx context.Context, projectID string) (ProjectUsage, error) {
	p := ProjectUsage{SampledAt: time.Now().UTC()}
	containers, err := c.engine.ListContainers(ctx, true, projectID)
	if err != nil {
		return p, err
	}
	samples := c.sample(ctx, containers)
	for i, ct := range containers {
		add(&p.Usage, ct, samples[i])
	}
	return p, nil
}

type sample struct {
	stats docker.Stats
	err   error
}

// sample reads the running containers, 8 at a time; the others get a zero sample.
func (c *Collector) sample(ctx context.Context, containers []docker.Container) []sample {
	out := make([]sample, len(containers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, ct := range containers {
		if ct.State != "running" {
			continue
		}
		wg.Add(1)
		go func(i int, ct docker.Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			// The containers come from a managed-only listing, so the listed labels
			// suffice as the guard; an extra inspect per container would only add latency.
			st, err := c.engine.ListedContainerStats(sctx, ct)
			if err != nil {
				c.log.Debug("container stats failed", "container", ct.Name, "err", err)
			}
			out[i] = sample{stats: st, err: err}
		}(i, ct)
	}
	wg.Wait()
	return out
}

// add counts one container and its sample into u.
func add(u *Usage, ct docker.Container, s sample) {
	u.Containers++
	if ct.State != "running" {
		return
	}
	u.Running++
	if s.err != nil {
		return
	}
	u.CPUPercent += s.stats.CPUPercent
	u.MemoryBytes += s.stats.MemoryBytes
	u.PerContainer = append(u.PerContainer, ContainerStats{ContainerID: ct.ID, Name: ct.Name, ProjectID: ct.ProjectID(), Service: ct.Service(),
		CPUPercent: s.stats.CPUPercent, MemoryBytes: s.stats.MemoryBytes, MemoryLimit: s.stats.MemoryLimit})
}

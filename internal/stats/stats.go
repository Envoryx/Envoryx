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
}

// Collector samples stats on demand and caches the result.
type Collector struct {
	engine docker.Engine
	log    *slog.Logger
	ttl    time.Duration

	mu      sync.Mutex
	last    Summary
	lastErr error
	inFly   bool
	waiters []chan struct{}
}

// New creates a collector with the given cache TTL.
func New(engine docker.Engine, ttl time.Duration, log *slog.Logger) *Collector {
	return &Collector{engine: engine, ttl: ttl, log: log}
}

// Summary returns cached stats, refreshing when older than the TTL. Concurrent callers
// share one refresh.
func (c *Collector) Summary(ctx context.Context) (Summary, error) {
	c.mu.Lock()
	if time.Since(c.last.SampledAt) < c.ttl {
		s, err := c.last, c.lastErr
		c.mu.Unlock()
		return s, err
	}
	if c.inFly {
		ch := make(chan struct{})
		c.waiters = append(c.waiters, ch)
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
		c.mu.Lock()
		s, err := c.last, c.lastErr
		c.mu.Unlock()
		return s, err
	}
	c.inFly = true
	c.mu.Unlock()

	s, err := c.collect(ctx)

	c.mu.Lock()
	c.last, c.lastErr = s, err
	c.inFly = false
	for _, w := range c.waiters {
		close(w)
	}
	c.waiters = nil
	c.mu.Unlock()
	return s, err
}

func (c *Collector) collect(ctx context.Context) (Summary, error) {
	s := Summary{PerProject: map[string]Usage{}, SampledAt: time.Now().UTC()}
	containers, err := c.engine.ListContainers(ctx, true, "")
	if err != nil {
		return s, err
	}
	s.Containers = len(containers)

	type result struct {
		c     docker.Container
		stats docker.Stats
		err   error
	}
	results := make([]result, len(containers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, ct := range containers {
		u := s.PerProject[ct.ProjectID()]
		u.Containers++
		s.PerProject[ct.ProjectID()] = u
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
			st, err := c.engine.ContainerStats(sctx, ct.ID)
			results[i] = result{c: ct, stats: st, err: err}
		}(i, ct)
	}
	wg.Wait()

	for i, ct := range containers {
		if ct.State != "running" {
			continue
		}
		r := results[i]
		u := s.PerProject[ct.ProjectID()]
		u.Running++
		s.Running++
		if r.err != nil {
			c.log.Debug("container stats failed", "container", ct.Name, "err", r.err)
		} else {
			u.CPUPercent += r.stats.CPUPercent
			u.MemoryBytes += r.stats.MemoryBytes
			s.CPUPercent += r.stats.CPUPercent
			s.MemoryBytes += r.stats.MemoryBytes
		}
		s.PerProject[ct.ProjectID()] = u
	}
	return s, nil
}

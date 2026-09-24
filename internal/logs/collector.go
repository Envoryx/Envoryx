package logs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// Engine is what the collector needs from Docker.
type Engine interface {
	ListContainers(ctx context.Context, managedOnly bool, projectID string) ([]docker.Container, error)
	StreamLogs(ctx context.Context, id string, opts docker.LogOptions, emit func(docker.LogLine)) error
}

// Collector copies the output of project containers into a Store. Every pass it follows
// the running containers it does not follow yet and reads a stopped container once, so
// a container that ran and stopped while Envoryx was down is caught up. It resumes each
// service after its newest stored line, which is also what keeps a recreated container
// from duplicating anything.
type Collector struct {
	Engine Engine
	Store  *Store
	// Collect selects the containers whose output is kept.
	Collect func(docker.Container) bool
	// Floor returns the oldest time worth collecting (retention, a clear); zero = all.
	Floor func() time.Time
	Log   *slog.Logger

	// FlushInterval and FlushLines bound how long and how many lines stay in memory.
	FlushInterval time.Duration
	FlushLines    int

	mu     sync.Mutex
	active map[string]context.CancelFunc // container id -> follower
	caught map[string]bool               // stopped containers already read to the end
	wg     sync.WaitGroup
}

// Pass follows new running containers and catches up stopped ones. With enabled false it
// stops every follower instead.
func (c *Collector) Pass(ctx context.Context, enabled bool) {
	c.mu.Lock()
	if c.active == nil {
		c.active, c.caught = map[string]context.CancelFunc{}, map[string]bool{}
	}
	c.mu.Unlock()
	if !enabled {
		c.StopAll()
		return
	}
	containers, err := c.Engine.ListContainers(ctx, true, "")
	if err != nil {
		c.Log.Debug("log history: list containers", "err", err)
		return
	}
	seen := map[string]bool{}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ctr := range containers {
		if !c.Collect(ctr) {
			continue
		}
		seen[ctr.ID] = true
		if _, ok := c.active[ctr.ID]; ok {
			continue
		}
		live := ctr.State == "running" || ctr.State == "restarting" || ctr.State == "paused"
		if live {
			delete(c.caught, ctr.ID)
		} else if c.caught[ctr.ID] || ctr.State == "created" {
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		c.active[ctr.ID] = cancel
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.collect(fctx, ctr, live)
			finished := fctx.Err() == nil // read to the end, not stopped by us
			cancel()
			c.mu.Lock()
			delete(c.active, ctr.ID)
			if !live && finished {
				c.caught[ctr.ID] = true
			}
			c.mu.Unlock()
		}()
	}
	for id := range c.caught {
		if !seen[id] {
			delete(c.caught, id)
		}
	}
}

// StopAll ends every follower and waits for their last lines to be written.
func (c *Collector) StopAll() {
	c.mu.Lock()
	for _, cancel := range c.active {
		cancel()
	}
	c.caught = map[string]bool{}
	c.mu.Unlock()
	c.wg.Wait()
}

// Following returns how many containers are being read right now.
func (c *Collector) Following() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}

// collect reads one container from its service's newest stored line on, until the
// stream ends (the container stopped, or follow is false) or ctx is cancelled.
func (c *Collector) collect(ctx context.Context, ctr docker.Container, follow bool) {
	k := Key{Project: ctr.ProjectID(), Service: ctr.Service()}
	since := c.Store.Last(k)
	if !since.IsZero() {
		since = since.Add(time.Nanosecond)
	}
	if c.Floor != nil {
		if f := c.Floor(); f.After(since) {
			since = f
		}
	}
	interval, maxLines := c.FlushInterval, c.FlushLines
	if interval <= 0 {
		interval = time.Second
	}
	if maxLines <= 0 {
		maxLines = 2000
	}

	var (
		mu      sync.Mutex
		buf     []docker.LogLine
		flushMu sync.Mutex // keeps batches in order when the ticker and a full buffer race
	)
	flush := func() {
		flushMu.Lock()
		defer flushMu.Unlock()
		mu.Lock()
		lines := buf
		buf = nil
		mu.Unlock()
		if err := c.Store.Append(k, lines); err != nil {
			c.Log.Warn("log history: write failed", "project", k.Project, "service", k.Service, "lines", len(lines), "err", err)
		}
	}
	done := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				flush()
			}
		}
	}()

	err := c.Engine.StreamLogs(ctx, ctr.ID, docker.LogOptions{Since: since, Follow: follow}, func(l docker.LogLine) {
		// Docker's since is inclusive and a line's time can repeat across a restart.
		if !since.IsZero() && !l.Time.IsZero() && l.Time.Before(since) {
			return
		}
		mu.Lock()
		buf = append(buf, l)
		full := len(buf) >= maxLines
		mu.Unlock()
		if full {
			flush()
		}
	})
	close(done)
	<-flushed
	flush()
	if err != nil && ctx.Err() == nil {
		c.Log.Debug("log history: stream ended", "container", ctr.Name, "err", err)
	}
}

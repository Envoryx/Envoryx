package project

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ServiceContainer resolves the container of a project service. The browser never
// addresses containers by id; this lookup is the only path from (project, kind) to a
// container and it only ever returns containers labelled for that project.
func (m *Manager) ServiceContainer(ctx context.Context, id string, kind store.ServiceKind) (docker.Container, error) {
	if err := validate.UUID(id); err != nil {
		return docker.Container{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return docker.Container{}, err
	}
	if wid, ok := strings.CutPrefix(string(kind), "worker:"); ok {
		found := false
		for _, w := range p.Workers {
			found = found || (w.ID == wid && w.Enabled)
		}
		if !found {
			return docker.Container{}, fmt.Errorf("%w: project has no such worker", store.ErrNotFound)
		}
	} else if svc := p.Service(kind); svc == nil || !svc.Enabled {
		return docker.Container{}, fmt.Errorf("%w: project has no %s service", store.ErrNotFound, kind)
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return docker.Container{}, err
	}
	for _, c := range containers {
		if c.Service() == string(kind) {
			return c, nil
		}
	}
	return docker.Container{}, fmt.Errorf("%w: the %s container does not exist; start the project", store.ErrNotFound, kind)
}

// StreamLogs streams the logs of a project service until ctx is cancelled.
func (m *Manager) StreamLogs(ctx context.Context, id string, kind store.ServiceKind, opts docker.LogOptions, emit func(docker.LogLine)) error {
	c, err := m.ServiceContainer(ctx, id, kind)
	if err != nil {
		return err
	}
	return m.engine.StreamLogs(ctx, c.ID, opts, emit)
}

// followPoll is how often FollowLogs looks for a replaced container, and followWait how
// long it waits for one after the stream ended while the project should be running.
var (
	followPoll = 2 * time.Second
	followWait = 2 * time.Minute
)

// FollowLogs streams a service's output like StreamLogs with Follow, and carries on with
// the successor when a restart or an update replaces the container: a follower attached
// to the old container would otherwise go quiet for good. It ends when ctx does, or when
// the service's container is gone and the project isn't meant to run.
func (m *Manager) FollowLogs(ctx context.Context, id string, kind store.ServiceKind, opts docker.LogOptions, emit func(docker.LogLine)) error {
	c, err := m.ServiceContainer(ctx, id, kind)
	if err != nil {
		return err
	}
	opts.Follow = true
	var last time.Time
	track := func(l docker.LogLine) {
		// Docker's since is inclusive: the lines already shown come again on a re-read.
		if !last.IsZero() && !l.Time.After(last) {
			return
		}
		if !l.Time.IsZero() {
			last = l.Time
		}
		emit(l)
	}
	for {
		sctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func(containerID string, o docker.LogOptions) {
			done <- m.engine.StreamLogs(sctx, containerID, o, track)
		}(c.ID, opts)
		next, err := m.awaitSuccessor(ctx, id, kind, c, done, cancel)
		cancel()
		if next.ID == "" || err != nil {
			return err
		}
		if next.ID == c.ID {
			// The same container again (started anew): only what came since.
			opts = docker.LogOptions{Follow: true, Since: last}
		} else {
			// A new container's output is all new.
			opts = docker.LogOptions{Follow: true}
		}
		c = next
	}
}

// awaitSuccessor watches a follow of c until it should move on. It returns the
// container to follow next (c itself when it was started again), or none when the
// follow is over. The stream has ended by the time it returns; stop ends it early.
func (m *Manager) awaitSuccessor(ctx context.Context, id string, kind store.ServiceKind, c docker.Container, done <-chan error, stop func()) (docker.Container, error) {
	tick := time.NewTicker(followPoll)
	defer tick.Stop()
	streaming := true
	var deadline time.Time
	for {
		select {
		case <-ctx.Done():
			if streaming {
				<-done
			}
			return docker.Container{}, nil
		case err := <-done:
			streaming = false
			if err != nil {
				return docker.Container{}, err
			}
			deadline = time.Now().Add(followWait)
			continue
		case <-tick.C:
		}
		cur, err := m.ServiceContainer(ctx, id, kind)
		replaced := err == nil && cur.ID != c.ID
		if streaming {
			if !replaced {
				continue
			}
			// The old stream may linger after its container was replaced; end it.
			stop()
			<-done
			return cur, nil
		}
		if replaced || (err == nil && cur.State == "running") {
			return cur, nil
		}
		p, perr := m.store.Projects.Get(ctx, id)
		if perr != nil || p.DesiredState != store.DesiredRunning || time.Now().After(deadline) {
			return docker.Container{}, nil
		}
	}
}

// LogPage is the result of a log query: the last lines that matched.
type LogPage struct {
	Lines []logs.Line `json:"lines"`
	// Matched counts every matching line in the range; Truncated says that only the
	// last len(Lines) of them are returned.
	Matched   int  `json:"matched"`
	Truncated bool `json:"truncated"`
	// Source is "history" (the stored history, across container restarts and
	// recreations) or "container" (what Docker still holds for the current container).
	Source string `json:"source"`
	// Oldest is the first stored line when Source is "history".
	Oldest time.Time `json:"oldest,omitzero"`
}

// Log sources.
const (
	LogSourceHistory   = "history"
	LogSourceContainer = "container"
)

// MaxLogLimit caps the lines a query returns; exports are not limited.
const MaxLogLimit = 10000

// scanLogs emits the lines of a service in the query's time range, oldest first, from
// the history when it holds the service, else from the container. An unfiltered query
// with tail > 0 lets Docker cut the history short.
func (m *Manager) scanLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, tail int, emit func(docker.LogLine)) (string, error) {
	k, ok, err := m.historyKey(ctx, id, kind)
	if err != nil {
		return "", err
	}
	if ok {
		return LogSourceHistory, m.logStore.Scan(ctx, k, q.Since, q.Until, emit)
	}
	opts := docker.LogOptions{Since: q.Since, Until: q.Until}
	if tail > 0 && !q.Filtered() {
		opts.Tail = strconv.Itoa(tail)
	}
	return LogSourceContainer, m.StreamLogs(ctx, id, kind, opts, emit)
}

// QueryLogs returns the last limit lines of a service that match q.
func (m *Manager) QueryLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, limit int) (LogPage, error) {
	if limit <= 0 || limit > MaxLogLimit {
		limit = 500
	}
	k, ok, err := m.historyKey(ctx, id, kind)
	if err != nil {
		return LogPage{}, err
	}
	if ok && !q.Filtered() && q.Since.IsZero() {
		// The newest lines of the whole history: read backwards, count the rest.
		lines, total, err := m.logStore.Tail(ctx, k, q.Until, limit)
		if err != nil {
			return LogPage{}, err
		}
		page := LogPage{Lines: make([]logs.Line, 0, len(lines)), Matched: total, Truncated: total > len(lines), Source: LogSourceHistory, Oldest: m.logStore.Oldest(ctx, k)}
		for _, l := range lines {
			page.Lines = append(page.Lines, logs.NewLine(l, logs.Classify(l.Text)))
		}
		return page, nil
	}
	ring := logs.NewRing(limit)
	match := logs.NewMatcher(q)
	source, err := m.scanLogs(ctx, id, kind, q, limit, func(l docker.LogLine) {
		if lvl, ok := match.Match(l); ok {
			ring.Add(logs.NewLine(l, lvl))
		}
	})
	if err != nil {
		return LogPage{}, err
	}
	lines := ring.Lines()
	page := LogPage{Lines: lines, Matched: ring.Total, Truncated: ring.Total > len(lines), Source: source}
	if source == LogSourceHistory {
		page.Oldest = m.logStore.Oldest(ctx, k)
	}
	return page, nil
}

// ExportLogs emits every line of a service that matches q, oldest first.
func (m *Manager) ExportLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, emit func(logs.Line)) error {
	match := logs.NewMatcher(q)
	_, err := m.scanLogs(ctx, id, kind, q, 0, func(l docker.LogLine) {
		if lvl, ok := match.Match(l); ok {
			emit(logs.NewLine(l, lvl))
		}
	})
	return err
}

// LogStats counts the lines, warnings and errors of a service over time and groups the
// most frequent problems.
func (m *Manager) LogStats(ctx context.Context, id string, kind store.ServiceKind, q logs.Query) (logs.Summary, error) {
	st := logs.NewStats()
	match := logs.NewMatcher(q)
	_, err := m.scanLogs(ctx, id, kind, q, 0, func(l docker.LogLine) {
		if lvl, ok := match.Match(l); ok {
			st.Add(logs.NewLine(l, lvl), lvl)
		}
	})
	if err != nil {
		return logs.Summary{}, err
	}
	until := q.Until
	if until.IsZero() && !q.Since.IsZero() {
		until = time.Now()
	}
	return st.Summary(q.Since, until, 10), nil
}

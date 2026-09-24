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

// LogPage is the result of a log query: the last lines that matched.
type LogPage struct {
	Lines []logs.Line `json:"lines"`
	// Matched counts every matching line in the range; Truncated says that only the
	// last len(Lines) of them are returned.
	Matched   int  `json:"matched"`
	Truncated bool `json:"truncated"`
}

// MaxLogLimit caps the lines a query returns; exports are not limited.
const MaxLogLimit = 10000

// scanLogs emits the lines of a service in the query's time range, oldest first. An
// unfiltered query with tail > 0 lets the source cut the history short.
func (m *Manager) scanLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, tail int, emit func(docker.LogLine)) error {
	opts := docker.LogOptions{Since: q.Since, Until: q.Until}
	if tail > 0 && !q.Filtered() {
		opts.Tail = strconv.Itoa(tail)
	}
	return m.StreamLogs(ctx, id, kind, opts, emit)
}

// QueryLogs returns the last limit lines of a service that match q.
func (m *Manager) QueryLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, limit int) (LogPage, error) {
	if limit <= 0 || limit > MaxLogLimit {
		limit = 500
	}
	ring := logs.NewRing(limit)
	match := logs.NewMatcher(q)
	err := m.scanLogs(ctx, id, kind, q, limit, func(l docker.LogLine) {
		if lvl, ok := match.Match(l); ok {
			ring.Add(logs.NewLine(l, lvl))
		}
	})
	if err != nil {
		return LogPage{}, err
	}
	lines := ring.Lines()
	return LogPage{Lines: lines, Matched: ring.Total, Truncated: ring.Total > len(lines)}, nil
}

// ExportLogs emits every line of a service that matches q, oldest first.
func (m *Manager) ExportLogs(ctx context.Context, id string, kind store.ServiceKind, q logs.Query, emit func(logs.Line)) error {
	match := logs.NewMatcher(q)
	return m.scanLogs(ctx, id, kind, q, 0, func(l docker.LogLine) {
		if lvl, ok := match.Match(l); ok {
			emit(logs.NewLine(l, lvl))
		}
	})
}

// LogStats counts the lines, warnings and errors of a service over time and groups the
// most frequent problems.
func (m *Manager) LogStats(ctx context.Context, id string, kind store.ServiceKind, q logs.Query) (logs.Summary, error) {
	st := logs.NewStats()
	match := logs.NewMatcher(q)
	err := m.scanLogs(ctx, id, kind, q, 0, func(l docker.LogLine) {
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

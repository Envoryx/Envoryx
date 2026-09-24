package project

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
)

// When a container reaches its memory limit the kernel kills a process in it. That may
// be the main process (the container restarts) or just a child – a php-fpm worker, a
// Node child, a queue job – while the container keeps running, which is easy to miss.
// Docker reports both as an "oom" event; the watcher turns them into a project warning
// for a day and a notification.

// oomWarningFor is how long an OOM kill stays on the project as a warning.
const oomWarningFor = 24 * time.Hour

type oomRecord struct {
	container string // what the warning names: php, node, worker queue …
	at        time.Time
	count     int
	limitMB   int
}

type oomLog struct {
	mu sync.Mutex
	// byProject maps project id → container name → the latest kill.
	byProject map[string]map[string]*oomRecord
}

func (m *Manager) ooms() *oomLog {
	m.oomOnce.Do(func() { m.oom = &oomLog{byProject: map[string]map[string]*oomRecord{}} })
	return m.oom
}

// RunOOMWatcher follows Docker's OOM events until ctx ends, reconnecting when the event
// stream breaks.
func (m *Manager) RunOOMWatcher(ctx context.Context, log *slog.Logger) {
	for {
		err := m.engine.WatchOOM(ctx, func(ev docker.OOMEvent) { m.recordOOM(ctx, ev) })
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("OOM watcher: event stream ended; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// oomName is how a warning names a container: the service kind, or "worker <name>".
func oomName(slug string, ev docker.OOMEvent) string {
	svc := ev.Labels[docker.LabelService]
	if strings.HasPrefix(svc, "worker:") {
		if name, ok := strings.CutPrefix(strings.TrimPrefix(ev.Name, "/"), "envoryx-"+slug+"-worker-"); ok {
			return "worker " + name
		}
		return "worker"
	}
	return svc
}

func (m *Manager) recordOOM(ctx context.Context, ev docker.OOMEvent) {
	projectID := ev.Labels[docker.LabelProjectID]
	if projectID == "" {
		return
	}
	p, err := m.store.Projects.Get(ctx, projectID)
	if err != nil {
		return
	}
	svc := ev.Labels[docker.LabelService]
	name := oomName(p.Slug, ev)
	limit := limitSet(p.Limits, store.ServiceKind(svc)).MemoryMB
	at := ev.Time
	if at.IsZero() {
		at = time.Now()
	}
	l := m.ooms()
	l.mu.Lock()
	recs := l.byProject[projectID]
	if recs == nil {
		recs = map[string]*oomRecord{}
		l.byProject[projectID] = recs
	}
	rec := recs[name]
	if rec == nil || at.Sub(rec.at) > oomWarningFor {
		rec = &oomRecord{container: name}
		recs[name] = rec
	}
	rec.at, rec.limitMB = at, limit
	rec.count++
	l.mu.Unlock()

	m.log.Warn("out of memory: the kernel killed a process", "project", p.Slug, "container", name, "limit_mb", limit)
	msg := fmt.Sprintf("The kernel killed a process in %s of %s because it ran out of memory", name, p.Name)
	if limit > 0 {
		msg += fmt.Sprintf(" (limit %d MiB). Raise the memory limit of the %s under Advanced → Resource limits, or find what uses that much.", limit, map[string]string{"app": "application containers", "services": "services"}[LimitGroup(store.ServiceKind(svc))])
	} else {
		msg += " – the host itself is out of memory. A memory limit per project keeps one project from starving the others."
	}
	m.notify(ctx, notify.Event{Kind: "project.oom", Level: notify.Warning, Project: p.Name, Title: fmt.Sprintf("%s of %s ran out of memory", name, p.Name), Message: msg, Key: "project.oom|" + projectID + "|" + name})
}

// oomWarnings are the recent OOM kills of a project, oldest first.
func (m *Manager) oomWarnings(projectID string) []string {
	l := m.ooms()
	l.mu.Lock()
	defer l.mu.Unlock()
	var recs []*oomRecord
	for _, r := range l.byProject[projectID] {
		if time.Since(r.at) <= oomWarningFor {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].at.Before(recs[j].at) })
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		when := r.at.Local().Format("15:04")
		switch {
		case r.limitMB > 0 && r.count > 1:
			out = append(out, fmt.Sprintf("%s ran out of memory %d times, last at %s (limit %d MiB); processes were killed", r.container, r.count, when, r.limitMB))
		case r.limitMB > 0:
			out = append(out, fmt.Sprintf("%s ran out of memory at %s (limit %d MiB); a process was killed", r.container, when, r.limitMB))
		case r.count > 1:
			out = append(out, fmt.Sprintf("%s ran out of memory %d times, last at %s; processes were killed", r.container, r.count, when))
		default:
			out = append(out, fmt.Sprintf("%s ran out of memory at %s; a process was killed", r.container, when))
		}
	}
	return out
}

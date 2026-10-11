package project

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// A hoster can suspend a managed instance (internal/plan): its projects are stopped like
// a user would stop them, so lifting the suspension starts nothing again, and every other
// operation is refused meanwhile (Manager.run). The schedulers pause, and whatever an
// operation that was already running brings up is stopped on the next pass.

// Suspension returns the hoster's suspension of the instance; nil when it has none.
func (m *Manager) Suspension() *plan.Suspension { return m.plan.Suspension() }

// suspensionChanged records the start and the end of a suspension in the audit log and
// has RunSuspension stop the projects.
func (m *Manager) suspensionChanged(s *plan.Suspension) {
	ctx := context.Background()
	if s != nil {
		m.audit.Log(ctx, audit.ActionInstanceSuspended, "instance", "", map[string]any{"since": s.Since, "message": s.Message})
	} else {
		m.audit.Log(ctx, audit.ActionInstanceUnsuspended, "instance", "", nil)
	}
	select {
	case m.suspendKick <- struct{}{}:
	default:
	}
}

// RunSuspension stops the projects of a suspended instance right after the suspension
// and checks every interval that nothing runs again, until ctx ends.
func (m *Manager) RunSuspension(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if m.Suspension() != nil {
			m.stopForSuspension(ctx, log)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.suspendKick:
		case <-t.C:
		}
	}
}

// stopForSuspension stops every project that wants to run or still has a running
// container, then the other managed containers (the database browser). A project busy
// with an operation is stopped on a later pass.
func (m *Manager) stopForSuspension(ctx context.Context, log *slog.Logger) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		log.Warn("suspension: load projects", "err", err)
		return
	}
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		log.Warn("suspension: docker unavailable", "err", err)
		return
	}
	running := map[string]bool{}
	for _, c := range containers {
		if c.State == "running" {
			running[c.Labels[docker.LabelProjectID]] = true
		}
	}
	for _, p := range projects {
		if p.Lifecycle == store.LifecycleDeleting || (p.DesiredState != store.DesiredRunning && !running[p.ID]) {
			continue
		}
		if _, err := m.Stop(ctx, p.ID); err != nil {
			if !errors.Is(err, ErrBusy) {
				log.Warn("suspension: stopping a project failed", "project", p.Name, "err", err)
			}
			continue
		}
		log.Info("suspension: project stopped", "project", p.Name)
	}
	m.stopManagedContainers(ctx, projects)
}

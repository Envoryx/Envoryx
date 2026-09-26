package project

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Resource limits cap every container of a project: the application containers (web
// server, PHP, Node, Python, workers) share one set, the services (database, caches,
// search, storage) another. Docker limits containers, not groups of them, so the numbers
// apply to each container of the group. Limits are not part of the spec fingerprint:
// changing them updates running containers in place (docker update) instead of
// recreating them – only lifting a limit entirely needs a new container.

// DefaultPidsLimit is the process limit of every container unless the project sets
// another: far above what a development stack runs, low enough to stop a fork bomb or a
// worker pool that keeps spawning.
const DefaultPidsLimit = 4096

// Bounds of what a project may set.
const (
	minMemoryMB = 64
	maxPids     = 1 << 20
	minPids     = 64
)

// LimitGroup names the set of limits a container kind falls under: "app" or "services".
func LimitGroup(kind store.ServiceKind) string {
	switch {
	case kind == store.ServiceWeb, kind == store.ServicePHP, kind == store.ServiceNode, kind == store.ServicePython, kind == store.ServiceGo, kind == store.ServiceRuby,
		strings.HasPrefix(string(kind), "worker:"):
		return "app"
	default:
		return "services"
	}
}

func limitSet(l store.ResourceLimits, kind store.ServiceKind) store.LimitSet {
	if LimitGroup(kind) == "app" {
		return l.App
	}
	return l.Services
}

// containerResources is what a container of kind runs with.
func containerResources(l store.ResourceLimits, kind store.ServiceKind) *docker.Resources {
	set := limitSet(l, kind)
	pids := l.Pids
	if pids == 0 {
		pids = DefaultPidsLimit
	}
	return &docker.Resources{
		NanoCPUs:    int64(math.Round(set.CPUs * 1e9)),
		MemoryBytes: int64(set.MemoryMB) << 20,
		PidsLimit:   int64(pids),
	}
}

// validateLimits checks limits against the host (hostCPUs/hostMemory 0 = unknown).
func validateLimits(l store.ResourceLimits, hostCPUs int, hostMemory int64) error {
	for name, set := range map[string]store.LimitSet{"application containers": l.App, "services": l.Services} {
		if set.CPUs < 0 || math.IsNaN(set.CPUs) {
			return fmt.Errorf("%w: CPU limit of the %s must not be negative", validate.ErrInvalid, name)
		}
		if set.CPUs > 0 && set.CPUs < 0.1 {
			return fmt.Errorf("%w: CPU limit of the %s must be at least 0.1 cores", validate.ErrInvalid, name)
		}
		if hostCPUs > 0 && set.CPUs > float64(hostCPUs) {
			return fmt.Errorf("%w: CPU limit of the %s is %.1f cores, the host has %d", validate.ErrInvalid, name, set.CPUs, hostCPUs)
		}
		if set.MemoryMB < 0 || (set.MemoryMB > 0 && set.MemoryMB < minMemoryMB) {
			return fmt.Errorf("%w: memory limit of the %s must be at least %d MiB", validate.ErrInvalid, name, minMemoryMB)
		}
		if hostMemory > 0 && int64(set.MemoryMB)<<20 > hostMemory {
			return fmt.Errorf("%w: memory limit of the %s is %d MiB, the host has %d MiB", validate.ErrInvalid, name, set.MemoryMB, hostMemory>>20)
		}
	}
	if l.Pids != 0 && (l.Pids < minPids || l.Pids > maxPids) {
		return fmt.Errorf("%w: the process limit must be between %d and %d", validate.ErrInvalid, minPids, maxPids)
	}
	return nil
}

// SetLimits stores a project's limits and applies them to its containers right away:
// running ones are updated in place, a lifted limit recreates the container.
func (m *Manager) SetLimits(ctx context.Context, id string, l store.ResourceLimits) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	l.App.CPUs = math.Round(l.App.CPUs*100) / 100
	l.Services.CPUs = math.Round(l.Services.CPUs*100) / 100
	var cpus int
	var mem int64
	if info, err := m.engine.Ping(ctx); err == nil {
		cpus, mem = info.NCPU, info.MemTotal
	}
	if err := validateLimits(l, cpus, mem); err != nil {
		return View{}, err
	}
	return m.runView(ctx, limitProvision, Operation{Action: "limits", ProjectID: id}, func(ctx context.Context) (View, error) {
		unlock, err := m.lock(id)
		if err != nil {
			return View{}, err
		}
		defer unlock()
		p, err := m.loadProject(ctx, id)
		if err != nil {
			return View{}, err
		}
		if err := m.store.Projects.SetLimits(ctx, id, l); err != nil {
			return View{}, err
		}
		p.Limits = l
		if err := m.applyResources(ctx, p); err != nil {
			return View{}, err
		}
		m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"limits": l}})
		return m.Get(ctx, id)
	})
}

// applyResources brings the limits of existing containers in line with the plan
// through ensurePlan, which also recreates a container whose limit was lifted. A running
// project stays running; the containers of a stopped one are only updated. Callers hold
// the project lock.
func (m *Manager) applyResources(ctx context.Context, p store.Project) error {
	planner, err := m.planner()
	if err != nil {
		return err
	}
	plan, err := planner.Plan(p)
	if err != nil {
		return err
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return nil // nothing created yet; the first start applies them
	}
	return m.ensurePlan(ctx, p, plan, p.DesiredState == store.DesiredRunning)
}

// syncResources updates a container whose limits differ from the plan and reports
// whether it has to be recreated instead (a lifted limit, or one the kernel refuses
// because the container uses more right now).
func (m *Manager) syncResources(ctx context.Context, cur docker.Container, want docker.Resources) (bool, error) {
	d, err := m.engine.InspectContainer(ctx, cur.ID)
	if err != nil {
		return false, err
	}
	if d.Resources == want {
		return false, nil
	}
	err = m.engine.UpdateResources(ctx, cur.ID, want)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, docker.ErrNeedsRecreate):
		return true, nil
	default:
		m.log.Warn("updating the limits failed, recreating the container", "container", cur.Name, "err", err)
		return true, nil
	}
}

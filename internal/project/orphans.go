package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/validate"
)

// ErrNotOrphan is returned when a resource to remove is not in the current orphan list.
var ErrNotOrphan = fmt.Errorf("%w: not an orphaned Envoryx resource", ErrNotFound)

// unclaimed reports whether a managed resource may belong to another Envoryx instance on
// the same Docker host: it has no instance label, so an older Envoryx created it, and it
// names no project of this instance. (What another instance labelled, the engine hides
// already.) Envoryx reports such resources as orphans but never stops or removes them on
// its own. Instance-wide helpers (LabelSystem) are not claimed by project and count as
// this instance's.
func unclaimed(labels map[string]string, known func(projectID string) bool) bool {
	if labels[docker.LabelInstance] != "" || labels[docker.LabelSystem] != "" {
		return false
	}
	return !known(labels[docker.LabelProjectID])
}

// OwnContainers drops the managed containers that may be another instance's (see
// unclaimed), for views that count or sample this instance's containers. When the
// projects cannot be read, nothing is dropped.
func (m *Manager) OwnContainers(ctx context.Context, containers []docker.Container) []docker.Container {
	known, err := m.knownProjects(ctx)
	if err != nil {
		return containers
	}
	out := containers[:0:0]
	for _, c := range containers {
		if !unclaimed(c.Labels, known) {
			out = append(out, c)
		}
	}
	return out
}

// knownProjects returns a lookup of this instance's project IDs.
func (m *Manager) knownProjects(ctx context.Context) (func(string) bool, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(projects))
	for _, p := range projects {
		ids[p.ID] = true
	}
	return func(id string) bool { return ids[id] }, nil
}

// cleanOrphans removes the orphaned containers and networks that the previous reconcile
// pass already reported and whose project isn't locked. A resource orphaned only for the
// moment (a project being created or rolled back) is left alone that way. Volumes hold data and
// are never removed automatically, and neither are unlabelled resources, which may be
// another instance's (see unclaimed). It returns what is still orphaned.
func (m *Manager) cleanOrphans(ctx context.Context, orphans []Orphan) []Orphan {
	previous := map[string]bool{}
	for _, o := range m.LastReport().Orphans {
		previous[o.Type+":"+o.ID] = true
	}
	var remaining, removed []Orphan
	// Containers first: a network cannot go while its containers are attached.
	for _, o := range orphans {
		if o.Type != "container" {
			continue
		}
		if o.Unlabelled || !previous[o.Type+":"+o.ID] || !m.removeOrphanUnlocked(ctx, o) {
			remaining = append(remaining, o)
			continue
		}
		removed = append(removed, o)
	}
	for _, o := range orphans {
		if o.Type == "container" {
			continue
		}
		if o.Type != "network" || o.Unlabelled || !previous[o.Type+":"+o.ID] || !m.removeOrphanUnlocked(ctx, o) {
			remaining = append(remaining, o)
			continue
		}
		removed = append(removed, o)
	}
	if len(removed) > 0 {
		names := make([]string, 0, len(removed))
		for _, o := range removed {
			names = append(names, o.Type+" "+o.Name)
		}
		m.log.Info("removed orphaned Envoryx resources", "count", len(removed), "resources", strings.Join(names, ", "))
		m.audit.Log(ctx, audit.ActionOrphansRemoved, "docker", "", map[string]any{"automatic": true, "removed": names})
		m.recordActivity(ActivityOrphansRemoved, names, "Orphaned resources removed",
			fmt.Sprintf("Envoryx removed %d Envoryx-labelled %s that belonged to no project: %s. Volumes are never removed automatically.",
				len(names), plural(len(names), "resource", "resources"), strings.Join(names, ", ")))
	}
	if remaining == nil {
		remaining = []Orphan{}
	}
	return remaining
}

// removeOrphanUnlocked removes an orphan unless its former project is busy; it reports
// whether the resource is gone. Failures are logged and retried on the next pass.
func (m *Manager) removeOrphanUnlocked(ctx context.Context, o Orphan) bool {
	if o.ProjectID != "" {
		unlock, err := m.lock(o.ProjectID)
		if err != nil {
			return false
		}
		defer unlock()
	}
	if err := m.removeOrphan(ctx, o); err != nil {
		m.log.Warn("remove orphaned resource", "type", o.Type, "name", o.Name, "err", err)
		return false
	}
	return true
}

// removeOrphan removes one orphaned resource. A network still used by a container that
// is not Envoryx's own is left alone.
func (m *Manager) removeOrphan(ctx context.Context, o Orphan) error {
	switch o.Type {
	case "container":
		if o.State == "running" {
			if err := m.engine.StopContainer(ctx, o.ID, m.cfg.StopTimeout); err != nil {
				return fmt.Errorf("stop container %s: %w", o.Name, err)
			}
		}
		return m.engine.RemoveContainer(ctx, o.ID)
	case "network":
		if err := m.detachProxy(ctx, o.Name); err != nil {
			return fmt.Errorf("detach proxy from %s: %w", o.Name, err)
		}
		if err := m.detachDBTool(ctx, o.Name); err != nil {
			return fmt.Errorf("detach database browser from %s: %w", o.Name, err)
		}
		endpoints, err := m.engine.NetworkEndpoints(ctx, o.Name)
		if err != nil {
			return fmt.Errorf("inspect network %s: %w", o.Name, err)
		}
		if len(endpoints) > 0 {
			names := make([]string, 0, len(endpoints))
			for _, ep := range endpoints {
				names = append(names, ep.Name)
			}
			return fmt.Errorf("%w: network %s is still used by %s", ErrConflict, o.Name, strings.Join(names, ", "))
		}
		return m.engine.RemoveNetwork(ctx, o.ID)
	case "volume":
		return m.engine.RemoveVolume(ctx, o.ID)
	default:
		return fmt.Errorf("%w: unknown resource type %q", validate.ErrInvalid, o.Type)
	}
}

// RemoveOrphan removes one resource from the current orphan list on the user's request,
// the only way an orphaned volume goes away. The list is refreshed afterwards.
func (m *Manager) RemoveOrphan(ctx context.Context, typ, id string) (ReconcileReport, error) {
	var target *Orphan
	for _, o := range m.LastReport().Orphans {
		if o.Type == typ && o.ID == id {
			target = &o
			break
		}
	}
	if target == nil {
		return ReconcileReport{}, fmt.Errorf("%w: %s %s", ErrNotOrphan, typ, id)
	}
	if target.ProjectID != "" {
		unlock, err := m.lock(target.ProjectID)
		if err != nil {
			return ReconcileReport{}, err
		}
		defer unlock()
	}
	if err := m.removeOrphan(ctx, *target); err != nil {
		return ReconcileReport{}, err
	}
	m.audit.Log(ctx, audit.ActionOrphansRemoved, "docker", "", map[string]any{"automatic": false, "removed": []string{target.Type + " " + target.Name}})
	return m.Reconcile(ctx), nil
}

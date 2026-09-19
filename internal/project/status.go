package project

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
)

// deriveStatus computes the observed state of a project from the managed container list.
// imageIDs (optional) maps image references to their current local id so containers built
// from an older build of the same tag can be flagged.
func deriveStatus(p store.Project, containers []docker.Container, imageIDs map[string]string) Status {
	st := Status{Services: []ServiceStatus{}, Warnings: []string{}}
	byKind := map[string]docker.Container{}
	for _, c := range containers {
		if c.ProjectID() == p.ID {
			byKind[c.Service()] = c
		}
	}
	enabled, running, existing := 0, 0, 0
	for _, svc := range p.Services {
		if !svc.Enabled {
			continue
		}
		enabled++
		ss := ServiceStatus{
			Kind: svc.Kind, Variant: svc.Variant, Version: svc.Version, Image: svc.Image,
			ContainerName: ContainerName(p.Slug, svc.Kind), State: "missing", Ports: []docker.PortMapping{},
		}
		if c, ok := byKind[string(svc.Kind)]; ok {
			existing++
			ss.Exists = true
			ss.ContainerID = c.ID
			ss.State = c.State
			ss.Status = c.Status
			ss.Health = c.Health
			ss.Ports = c.Ports
			if c.State == "running" {
				running++
				ss.Running = true
			}
			rec := p.ImageRecord(svc.Image)
			switch {
			case rec != nil && rec.Pinned && rec.PreviousID != "":
				// Rolled back on purpose: the container runs the previous id.
				if c.ImageID != "" && c.ImageID != rec.PreviousID {
					st.Warnings = append(st.Warnings, fmt.Sprintf("%s is rolled back to the previous image but the container runs another one; restart to apply", svc.Kind))
				}
			case c.Image != svc.Image && c.Image != c.ImageID:
				st.Warnings = append(st.Warnings, fmt.Sprintf("%s container uses image %s but %s is configured; restart to apply", svc.Kind, c.Image, svc.Image))
			default:
				// Docker lists the container's image as its id once the tag moved on.
				localID, ok := imageIDs[svc.Image]
				if c.Image == c.ImageID || (ok && c.ImageID != "" && localID != c.ImageID) {
					st.Warnings = append(st.Warnings, fmt.Sprintf("a newer %s image was pulled; restart to apply", svc.Kind))
				}
			}
		}
		if rec := p.ImageRecord(svc.Image); rec != nil && rec.PreviousID != "" {
			t := rec.ChangedAt
			ss.ImageChangedAt = &t
			ss.ImagePrevious = true
			ss.ImagePinned = rec.Pinned
		}
		st.Services = append(st.Services, ss)
	}
	workerKinds := map[string]bool{}
	if php := p.Service(store.ServicePHP); php != nil && php.Enabled {
		for _, w := range p.Workers {
			workerKinds[string(WorkerKind(w))] = true
			if !w.Enabled {
				continue
			}
			enabled++
			ss := ServiceStatus{Kind: "worker", Variant: w.Name, Version: w.Preset, Image: php.Image, ContainerName: WorkerContainerName(p.Slug, w), State: "missing", Ports: []docker.PortMapping{}, WorkerID: w.ID}
			if c, ok := byKind[string(WorkerKind(w))]; ok {
				existing++
				ss.Exists, ss.ContainerID, ss.State, ss.Status, ss.Health, ss.Ports = true, c.ID, c.State, c.Status, c.Health, c.Ports
				if c.State == "running" {
					running++
					ss.Running = true
				}
			}
			st.Services = append(st.Services, ss)
		}
	}
	for kind := range byKind {
		if strings.HasPrefix(kind, "worker:") {
			if !workerKinds[kind] {
				st.Warnings = append(st.Warnings, "container for a removed worker still exists")
			}
			continue
		}
		if p.Service(store.ServiceKind(kind)) == nil {
			st.Warnings = append(st.Warnings, fmt.Sprintf("container for removed service %q still exists", kind))
		}
	}

	switch {
	case p.Lifecycle == store.LifecycleCreating:
		st.State = StateCreating
	case p.Lifecycle == store.LifecycleDeleting:
		st.State = StateDeleting
	case p.Lifecycle == store.LifecycleFailed:
		st.State = StateError
	case enabled == 0:
		st.State = StateStopped
	case running == enabled:
		st.State = StateRunning
	case running > 0:
		st.State = StatePartial
	case existing == 0:
		st.State = StateMissing
	default:
		st.State = StateStopped
	}
	if p.LastError != "" {
		st.Warnings = append(st.Warnings, p.LastError)
	}
	if p.DesiredState == store.DesiredRunning && st.State != StateRunning && p.Lifecycle == store.LifecycleReady {
		st.Warnings = append(st.Warnings, "project should be running but is "+string(st.State))
	}
	return st
}

// Reconcile compares the database with Docker, records inconsistencies and orphaned
// resources, and repairs interrupted lifecycles. It never removes anything.
func (m *Manager) Reconcile(ctx context.Context) ReconcileReport {
	report := ReconcileReport{At: time.Now().UTC(), Orphans: []Orphan{}, Issues: []ReconcileIssue{}, States: map[string]Status{}}
	projects, err := m.loadProjects(ctx)
	if err != nil {
		report.Error = err.Error()
		m.setReport(report)
		return report
	}
	report.Projects = len(projects)
	known := map[string]store.Project{}
	for _, p := range projects {
		known[p.ID] = p
	}

	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		report.Error = "docker: " + err.Error()
		m.setReport(report)
		return report
	}
	networks, _ := m.engine.ListNetworks(ctx, true)
	volumes, _ := m.engine.ListVolumes(ctx, true)

	for _, p := range projects {
		// A restart in the middle of create/delete leaves a transitional lifecycle behind.
		// While the operation is still running it holds the project lock – a create that
		// takes a minute to pull images must not be declared interrupted by the periodic
		// reconcile that happens to run meanwhile.
		if p.Lifecycle == store.LifecycleCreating || p.Lifecycle == store.LifecycleDeleting {
			if unlock, err := m.lock(p.ID); err == nil {
				msg := fmt.Sprintf("%s was interrupted by an Envoryx restart; review the project and retry or delete it", p.Lifecycle)
				if err := m.store.Projects.UpdateState(ctx, p.ID, p.DesiredState, store.LifecycleFailed, msg); err == nil {
					p.Lifecycle = store.LifecycleFailed
					p.LastError = msg
				}
				unlock()
				report.Issues = append(report.Issues, ReconcileIssue{ProjectID: p.ID, ProjectName: p.Name, Severity: "error", Message: msg})
			}
		}
		st := deriveStatus(p, containers, m.localImageIDs(ctx, projects))
		report.States[p.ID] = st
		if p.DesiredState == store.DesiredRunning && st.State != StateRunning && p.Lifecycle == store.LifecycleReady {
			report.Issues = append(report.Issues, ReconcileIssue{ProjectID: p.ID, ProjectName: p.Name, Severity: "warning",
				Message: fmt.Sprintf("expected running but observed %s", st.State)})
		}
		if st.State == StateMissing && p.Lifecycle == store.LifecycleReady {
			report.Issues = append(report.Issues, ReconcileIssue{ProjectID: p.ID, ProjectName: p.Name, Severity: "error",
				Message: "no containers exist for this project; start it to recreate them"})
		}
	}

	m.notifyHealth(ctx, projects, report.Issues)

	for _, c := range containers {
		if c.Labels[docker.LabelSystem] != "" {
			continue // instance-wide helpers (database browser) belong to no project
		}
		if _, ok := known[c.ProjectID()]; !ok {
			report.Orphans = append(report.Orphans, Orphan{Type: "container", ID: c.ID, Name: c.Name, ProjectID: c.ProjectID(), ProjectName: c.Labels[docker.LabelProjectName], State: c.State, Created: c.Created})
		}
	}
	for _, n := range networks {
		if n.Labels[docker.LabelSystem] != "" {
			continue
		}
		if _, ok := known[n.Labels[docker.LabelProjectID]]; !ok {
			report.Orphans = append(report.Orphans, Orphan{Type: "network", ID: n.ID, Name: n.Name, ProjectID: n.Labels[docker.LabelProjectID], ProjectName: n.Labels[docker.LabelProjectName]})
		}
	}
	for _, v := range volumes {
		if _, ok := known[v.Labels[docker.LabelProjectID]]; !ok {
			report.Orphans = append(report.Orphans, Orphan{Type: "volume", ID: v.Name, Name: v.Name, ProjectID: v.Labels[docker.LabelProjectID], ProjectName: v.Labels[docker.LabelProjectName]})
		}
	}
	m.AttachProxyToAll(ctx)
	for _, issue := range report.Issues {
		m.log.Warn("reconcile issue", "project", issue.ProjectName, "severity", issue.Severity, "msg", issue.Message)
	}
	if len(report.Orphans) > 0 {
		m.log.Warn("reconcile found orphaned Envoryx resources", "count", len(report.Orphans))
	}
	m.setReport(report)
	return report
}

// localImageIDs looks up the local ids of all images used by the given projects.
func (m *Manager) localImageIDs(ctx context.Context, projects []store.Project) map[string]string {
	ids := map[string]string{}
	for _, p := range projects {
		for _, svc := range p.Services {
			if !svc.Enabled || svc.Image == "" {
				continue
			}
			if _, done := ids[svc.Image]; done {
				continue
			}
			if id, err := m.engine.ImageID(ctx, svc.Image); err == nil {
				ids[svc.Image] = id
			} else {
				ids[svc.Image] = ""
			}
		}
	}
	for k, v := range ids {
		if v == "" {
			delete(ids, k)
		}
	}
	return ids
}

func (m *Manager) setReport(r ReconcileReport) {
	m.reportMu.Lock()
	m.report = r
	m.reportMu.Unlock()
}

// LastReport returns the most recent reconciliation report.
func (m *Manager) LastReport() ReconcileReport {
	m.reportMu.RLock()
	defer m.reportMu.RUnlock()
	return m.report
}

// notifyHealth sends one event when a project becomes unhealthy and one when it recovers.
func (m *Manager) notifyHealth(ctx context.Context, projects []store.Project, issues []ReconcileIssue) {
	if m.notifier == nil {
		return
	}
	m.reportMu.Lock()
	if m.unhealthy == nil {
		m.unhealthy = map[string]bool{}
	}
	current := map[string]ReconcileIssue{}
	for _, is := range issues {
		if _, dup := current[is.ProjectID]; !dup {
			current[is.ProjectID] = is
		}
	}
	var events []notify.Event
	for id, is := range current {
		if !m.unhealthy[id] {
			m.unhealthy[id] = true
			level := notify.Warning
			if is.Severity == "error" {
				level = notify.Error
			}
			events = append(events, notify.Event{Kind: "project.unhealthy", Level: level, Project: is.ProjectName, Title: is.ProjectName + " needs attention", Message: is.Message, Key: "project.unhealthy|" + id})
		}
	}
	for id := range m.unhealthy {
		if _, still := current[id]; still {
			continue
		}
		delete(m.unhealthy, id)
		name := id
		for _, p := range projects {
			if p.ID == id {
				name = p.Name
			}
		}
		m.notifier.Clear("project.unhealthy|" + id)
		events = append(events, notify.Event{Kind: "project.unhealthy", Level: notify.Info, Project: name, Title: name + " recovered", Message: "The project is running again.", Key: "project.recovered|" + id})
	}
	m.reportMu.Unlock()
	for _, e := range events {
		m.notifier.Notify(ctx, e)
	}
}

// RunReconciler reconciles immediately and then periodically until ctx is cancelled.
func (m *Manager) RunReconciler(ctx context.Context, interval time.Duration, log *slog.Logger) {
	m.Reconcile(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Reconcile(ctx)
		}
	}
}

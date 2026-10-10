package project

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// A hoster's plan (internal/plan) limits what a managed instance may do. The checks here
// run before a project, a service or a backup comes into being; whatever exists when a
// plan arrives or shrinks stays as it is.

// runtimeKinds are the services a plan's runtimes refer to.
var runtimeKinds = []store.ServiceKind{store.ServicePHP, store.ServiceNode, store.ServicePython, store.ServiceGo, store.ServiceRuby, store.ServiceJava, store.ServiceDotnet}

// planUsage is the measured disk space of the projects, for the plan's disk limit.
type planUsage struct {
	mu         sync.Mutex
	diskBytes  int64
	measuredAt time.Time
}

// SetPlan hands the Manager the instance's plan.
func (m *Manager) SetPlan(h *plan.Holder) { m.plan = h }

// Plan returns the instance's plan; nil when it has none.
func (m *Manager) Plan() *plan.Plan { return m.plan.Get() }

// checkNewProject is the plan's say on a project about to be created (also a duplicate,
// a branch environment or a restore into a new project).
func (m *Manager) checkNewProject(ctx context.Context, p store.Project) error {
	pl := m.Plan()
	if pl == nil {
		return nil
	}
	if max := pl.Limits.Projects; max > 0 {
		n, err := m.store.Projects.Count(ctx)
		if err != nil {
			return err
		}
		if n >= max {
			return plan.Quota("this instance has reached the plan's project limit (%d)", max)
		}
	}
	if err := m.checkDiskQuota(); err != nil {
		return err
	}
	return checkProjectPlan(pl, p, nil)
}

// checkProjectPlan checks what a project brings in compared to prev (nil for a new
// project): runtimes, addons, external services, custom images, the IDE gateway.
func checkProjectPlan(pl *plan.Plan, p store.Project, prev *store.Project) error {
	if pl == nil {
		return nil
	}
	had := func(kind store.ServiceKind) *store.ProjectService {
		if prev == nil {
			return nil
		}
		return prev.Service(kind)
	}
	for i := range p.Services {
		svc := &p.Services[i]
		old := had(svc.Kind)
		if old == nil && slices.Contains(runtimeKinds, svc.Kind) {
			if err := pl.RequireRuntime(string(svc.Kind)); err != nil {
				return err
			}
		}
		if old == nil && svc.Kind.IsAddon() {
			if err := pl.RequireFeature(plan.FeatureAddons); err != nil {
				return err
			}
		}
		if externalService(svc) && !externalService(old) {
			if err := pl.RequireFeature(plan.FeatureExternalServices); err != nil {
				return err
			}
		}
		if svc.Custom.Set() && (old == nil || !old.Custom.Set()) {
			if err := pl.RequireFeature(plan.FeatureCustomImages); err != nil {
				return err
			}
		}
	}
	if p.IDEGateway && (prev == nil || !prev.IDEGateway) {
		if err := pl.RequireFeature(plan.FeatureIDEGateway); err != nil {
			return err
		}
	}
	if p.Branches.Watch && (prev == nil || !prev.Branches.Watch) {
		if err := pl.RequireFeature(plan.FeatureBranchEnvs); err != nil {
			return err
		}
	}
	return nil
}

// checkUpdatePlan checks what an update brings in before any of it is stored.
func (m *Manager) checkUpdatePlan(proj store.Project, req UpdateRequest) error {
	pl := m.Plan()
	if pl == nil {
		return nil
	}
	adds := map[store.ServiceKind]bool{
		store.ServicePHP:    req.PHP != nil && req.PHP.Enabled,
		store.ServiceNode:   req.Node != nil && req.Node.Enabled,
		store.ServicePython: req.Python != nil && req.Python.Enabled,
		store.ServiceGo:     req.Go != nil && req.Go.Enabled,
		store.ServiceRuby:   req.Ruby != nil && req.Ruby.Enabled,
		store.ServiceJava:   req.Java != nil && req.Java.Enabled,
		store.ServiceDotnet: req.Dotnet != nil && req.Dotnet.Enabled,
	}
	for _, kind := range runtimeKinds {
		if adds[kind] && proj.Service(kind) == nil {
			if err := pl.RequireRuntime(string(kind)); err != nil {
				return err
			}
		}
	}
	for name, a := range req.Addons {
		if a.Enabled && proj.Service(store.AddonKind(name)) == nil {
			if err := pl.RequireFeature(plan.FeatureAddons); err != nil {
				return err
			}
		}
	}
	external := (req.Database != nil && req.Database.External != nil) || (req.Redis != nil && req.Redis.External != nil)
	for _, d := range req.Databases {
		external = external || d.External != nil
	}
	if external {
		if err := pl.RequireFeature(plan.FeatureExternalServices); err != nil {
			return err
		}
	}
	if req.IDEGateway != nil && *req.IDEGateway && !proj.IDEGateway {
		if err := pl.RequireFeature(plan.FeatureIDEGateway); err != nil {
			return err
		}
	}
	return nil
}

// requirePlanFeature returns the plan's refusal of a feature, nil when it is included.
func (m *Manager) requirePlanFeature(f plan.Feature) error { return m.Plan().RequireFeature(f) }

// checkDiskQuota refuses new projects and backups once the projects take the plan's
// disk space.
func (m *Manager) checkDiskQuota() error {
	limit := m.Plan().DiskBytes()
	if limit == 0 {
		return nil
	}
	m.usage.mu.Lock()
	used := m.usage.diskBytes
	m.usage.mu.Unlock()
	if used >= limit {
		return plan.Quota("the projects take %s of the %s disk space in the plan; free some space (old backups, unused projects) first", disk.Human(uint64(used)), disk.Human(uint64(limit)))
	}
	return nil
}

// DiskUsage returns the disk space the projects took at the last measurement: their
// directories, volumes and backups.
func (m *Manager) DiskUsage() (int64, time.Time) {
	m.usage.mu.Lock()
	defer m.usage.mu.Unlock()
	return m.usage.diskBytes, m.usage.measuredAt
}

// measureDisk adds up the project directories, the volumes of this instance and the
// backups directory.
func (m *Manager) measureDisk(ctx context.Context) error {
	paths, err := m.paths()
	if err != nil {
		return err
	}
	total := dirSize(paths.ProjectsDir) + dirSize(paths.BackupsRoot())
	if vols, err := m.engine.VolumeSizes(ctx); err == nil {
		for _, v := range vols {
			if v.Bytes > 0 {
				total += v.Bytes
			}
		}
	}
	m.usage.mu.Lock()
	m.usage.diskBytes, m.usage.measuredAt = total, time.Now()
	m.usage.mu.Unlock()
	return nil
}

// RunDiskUsage measures the disk space of the projects every interval while the plan
// limits it.
func (m *Manager) RunDiskUsage(ctx context.Context, interval time.Duration) {
	for {
		if m.Plan().DiskBytes() > 0 {
			if err := m.measureDisk(ctx); err != nil {
				m.log.Debug("plan: measuring disk space", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

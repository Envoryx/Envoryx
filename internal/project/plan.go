package project

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/runtime"
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
	// container are the plan's container limits and defaults last applied.
	container [2]plan.ContainerLimits
}

// SetPlan hands the Manager the instance's plan and suspension.
func (m *Manager) SetPlan(h *plan.Holder) {
	m.plan = h
	// A hosted instance's admins don't get to reach the hoster's network through an
	// addon URL.
	m.addons.SetPrivateAllowed(func() bool { return h.Get() == nil })
	h.OnSuspend(m.suspensionChanged)
	// The containers already run with the limits of the plan they started under.
	if pl := h.Get(); pl != nil {
		m.usage.container = [2]plan.ContainerLimits{pl.Limits.Container, pl.Limits.ContainerDefault}
	}
	h.OnChange(m.planChanged)
}

// planChanged brings the containers in line with new container limits of the plan, in
// the background; a project busy with something else gets them on its next start.
func (m *Manager) planChanged(pl *plan.Plan) {
	var c, d plan.ContainerLimits
	if pl != nil {
		c, d = pl.Limits.Container, pl.Limits.ContainerDefault
	}
	m.usage.mu.Lock()
	same := m.usage.container == [2]plan.ContainerLimits{c, d}
	m.usage.container = [2]plan.ContainerLimits{c, d}
	m.usage.mu.Unlock()
	if same {
		return
	}
	go func() {
		ctx := context.Background()
		projects, err := m.store.Projects.List(ctx)
		if err != nil {
			return
		}
		for _, p := range projects {
			if p.Lifecycle != store.LifecycleReady {
				continue
			}
			unlock, err := m.lock(p.ID)
			if err != nil {
				continue
			}
			if p, err := m.loadProject(ctx, p.ID); err == nil {
				if err := m.applyResources(ctx, p); err != nil {
					m.log.Warn("plan: container limits not applied", "project", p.Slug, "err", err)
				}
			}
			unlock()
		}
	}()
}

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
	if p.DesiredState == store.DesiredRunning {
		if err := m.checkRunningQuota(ctx, p.ID); err != nil {
			return err
		}
	}
	for _, svc := range p.Services {
		if err := m.requireService(pl, svc); err != nil {
			return err
		}
	}
	return checkProjectPlan(pl, p, nil, m.catalogVersion)
}

// RunningProjects counts the projects meant to run now.
func (m *Manager) RunningProjects(ctx context.Context) (int, error) {
	return m.runningProjects(ctx, "")
}

// runningProjects counts the projects meant to run, other than except.
func (m *Manager) runningProjects(ctx context.Context, except string) (int, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range projects {
		if p.ID != except && p.DesiredState == store.DesiredRunning {
			n++
		}
	}
	return n, nil
}

// checkRunningQuota refuses to start another project (not id) once the plan's number
// of running projects is reached.
func (m *Manager) checkRunningQuota(ctx context.Context, id string) error {
	pl := m.Plan()
	if pl == nil || pl.Limits.RunningProjects == 0 {
		return nil
	}
	max := pl.Limits.RunningProjects
	n, err := m.runningProjects(ctx, id)
	if err != nil {
		return err
	}
	if n >= max {
		return plan.Quota("this instance's plan runs at most %d projects at the same time; stop another project first", max)
	}
	return nil
}

// planService is the name a plan's services give a service (plan.ServiceNames), "" for
// one a plan doesn't restrict (runtimes, the web server, addons).
func planService(svc store.ProjectService) string {
	switch {
	case svc.Kind.IsDatabase():
		return svc.Variant
	case svc.Kind == store.ServiceStorage:
		return "storage"
	case svc.Kind == store.ServiceOpenSearchDashboards:
		return "opensearch"
	}
	switch svc.Kind {
	case store.ServiceRedis, store.ServiceMemcached, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOllama, store.ServiceMailpit:
		return string(svc.Kind)
	}
	return ""
}

// requireService is the plan's say on a project getting svc (not run by Envoryx when
// external).
func (m *Manager) requireService(pl *plan.Plan, svc store.ProjectService) error {
	name := planService(svc)
	if name == "" || pl.AllowsService(name) || externalService(&svc) {
		return nil
	}
	label := name
	key := name
	if key == "storage" {
		key = "rustfs"
	}
	if m.catalog != nil {
		if r, ok := m.catalog.Get(key); ok {
			label = r.Name
		}
	}
	return pl.RequireService(name, label)
}

// versionLookup finds the catalogue entry of a service's version (Manager.catalogVersion).
type versionLookup func(svc store.ProjectService) (runtime.Version, runtime.Runtime, bool)

// checkProjectPlan checks what a project brings in compared to prev (nil for a new
// project): runtimes, addons, external services, custom images, the IDE gateway, and
// versions past their end of life or not released yet.
func checkProjectPlan(pl *plan.Plan, p store.Project, prev *store.Project, lookup versionLookup) error {
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
		if !svc.Custom.Set() && (old == nil || old.Variant != svc.Variant || old.Version != svc.Version) {
			if err := checkVersionPlan(pl, *svc, lookup); err != nil {
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
	if err := m.checkUpdateServices(pl, proj, req); err != nil {
		return err
	}
	return m.checkUpdateVersions(pl, proj, req)
}

// checkUpdateServices checks the services and database engines an update adds.
func (m *Manager) checkUpdateServices(pl *plan.Plan, proj store.Project, req UpdateRequest) error {
	if pl == nil || len(pl.Services) == 0 {
		return nil
	}
	var adds []store.ProjectService
	db := func(kind store.ServiceKind, d DatabaseUpdate) {
		if !d.Enabled || d.External != nil {
			return
		}
		cur := proj.Service(kind)
		typ := d.Type
		if typ == "" && cur != nil {
			typ = cur.Variant
		}
		if cur == nil || cur.Variant != typ || externalService(cur) {
			adds = append(adds, store.ProjectService{Kind: kind, Variant: typ})
		}
	}
	if req.Database != nil {
		db(store.ServiceDatabase, *req.Database)
	}
	for name, d := range req.Databases {
		db(store.DatabaseKind(name), d)
	}
	extras := map[store.ServiceKind]*ExtraUpdate{
		store.ServiceRedis: req.Redis, store.ServiceMemcached: req.Memcached, store.ServiceMailpit: req.Mailpit,
		store.ServiceRabbitMQ: req.RabbitMQ, store.ServiceMeilisearch: req.Meilisearch, store.ServiceTypesense: req.Typesense,
		store.ServiceOllama: req.Ollama, store.ServiceOpenSearch: req.OpenSearch,
	}
	for kind, e := range extras {
		if e == nil || e.External != nil || e.Enabled == nil || !*e.Enabled {
			continue
		}
		if cur := proj.Service(kind); cur == nil || externalService(cur) {
			adds = append(adds, store.ProjectService{Kind: kind})
		}
	}
	if st := req.Storage; st != nil && st.Enabled && proj.Service(store.ServiceStorage) == nil {
		adds = append(adds, store.ProjectService{Kind: store.ServiceStorage})
	}
	for _, svc := range adds {
		if err := m.requireService(pl, svc); err != nil {
			return err
		}
	}
	return nil
}

// checkVersionPlan refuses a version the plan leaves out: one past its end of life or a
// pre-release. Only a version a service is about to get is checked; one it runs already
// stays.
func checkVersionPlan(pl *plan.Plan, svc store.ProjectService, lookup versionLookup) error {
	if pl == nil || lookup == nil {
		return nil
	}
	v, r, ok := lookup(svc)
	if !ok {
		return nil
	}
	if v.EOL && !pl.Allows(plan.FeatureEOLVersions) {
		return plan.Quota("%s %s has reached its end of life; this instance's plan includes supported versions only", r.Name, v.Version)
	}
	if v.Preview && !pl.Allows(plan.FeaturePreviewVersions) {
		return plan.Quota("%s %s is a preview version; this instance's plan includes released versions only", r.Name, v.Version)
	}
	return nil
}

// checkUpdateVersions checks the versions an update asks for that the project doesn't
// run yet: another runtime, web server, database or service version, or a new service.
func (m *Manager) checkUpdateVersions(pl *plan.Plan, proj store.Project, req UpdateRequest) error {
	if pl == nil || (pl.Allows(plan.FeatureEOLVersions) && pl.Allows(plan.FeaturePreviewVersions)) {
		return nil
	}
	check := func(kind store.ServiceKind, variant, version string) error {
		cur := proj.Service(kind)
		if cur != nil {
			if variant == "" {
				variant = cur.Variant
			}
			if version == "" && variant == cur.Variant {
				return nil // kept as it is
			}
			if cur.Custom.Set() || (variant == cur.Variant && version == cur.Version) {
				return nil
			}
		}
		svc := store.ProjectService{Kind: kind, Variant: variant, Version: version}
		if version == "" {
			// A new service or another server without a version gets the default one.
			key := catalogKey(svc)
			if key == "" {
				return nil
			}
			v, err := m.catalog.Resolve(key, "")
			if err != nil {
				return nil
			}
			svc.Version = v.Version
		}
		return checkVersionPlan(pl, svc, m.catalogVersion)
	}
	type want struct {
		kind    store.ServiceKind
		version string
	}
	var runtimes []want
	if u := req.PHP; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServicePHP, u.Version})
	}
	if u := req.Node; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServiceNode, u.Version})
	}
	if u := req.Python; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServicePython, u.Version})
	}
	if u := req.Go; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServiceGo, u.Version})
	}
	if u := req.Ruby; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServiceRuby, u.Version})
	}
	if u := req.Java; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServiceJava, u.Version})
	}
	if u := req.Dotnet; u != nil && u.Enabled {
		runtimes = append(runtimes, want{store.ServiceDotnet, u.Version})
	}
	for _, rt := range runtimes {
		if err := check(rt.kind, "", rt.version); err != nil {
			return err
		}
	}
	if w := req.Web; w != nil {
		if err := check(store.ServiceWeb, w.Type, w.Version); err != nil {
			return err
		}
	}
	if d := req.Database; d != nil && d.Enabled && d.External == nil {
		if err := check(store.ServiceDatabase, d.Type, d.Version); err != nil {
			return err
		}
	}
	for name, d := range req.Databases {
		if d.Enabled && d.External == nil {
			if err := check(store.DatabaseKind(name), d.Type, d.Version); err != nil {
				return err
			}
		}
	}
	extras := map[store.ServiceKind]*ExtraUpdate{
		store.ServiceRedis: req.Redis, store.ServiceMemcached: req.Memcached, store.ServiceMailpit: req.Mailpit,
		store.ServiceRabbitMQ: req.RabbitMQ, store.ServiceMeilisearch: req.Meilisearch, store.ServiceTypesense: req.Typesense,
		store.ServiceOllama: req.Ollama, store.ServiceOpenSearch: req.OpenSearch,
	}
	for kind, e := range extras {
		if e == nil || (e.Enabled != nil && !*e.Enabled) || (e.Enabled == nil && e.Version == "") {
			continue
		}
		if err := check(kind, "", e.Version); err != nil {
			return err
		}
	}
	if st := req.Storage; st != nil && st.Enabled {
		if err := check(store.ServiceStorage, "rustfs", st.Version); err != nil {
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

// RunDiskUsage measures the disk space of the projects every interval while the
// instance has a plan.
func (m *Manager) RunDiskUsage(ctx context.Context, interval time.Duration) {
	for {
		if m.Plan() != nil {
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

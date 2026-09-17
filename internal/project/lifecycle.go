package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

// journal records created Docker resources so a failed operation can be rolled back.
type journal struct {
	containers []string // ids
	volumes    []string
	network    string
	configDir  string
}

// rollback removes journaled resources in reverse order. Every removal is label-guarded by
// the engine. Errors are collected and returned, nothing is hidden.
func (m *Manager) rollback(ctx context.Context, j journal) error {
	ctx = context.WithoutCancel(ctx)
	var errs []error
	for i := len(j.containers) - 1; i >= 0; i-- {
		if err := m.engine.RemoveContainer(ctx, j.containers[i]); err != nil {
			errs = append(errs, fmt.Errorf("remove container %s: %w", j.containers[i], err))
		}
	}
	for i := len(j.volumes) - 1; i >= 0; i-- {
		if err := m.engine.RemoveVolume(ctx, j.volumes[i]); err != nil {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", j.volumes[i], err))
		}
	}
	if j.network != "" {
		if err := m.engine.RemoveNetwork(ctx, j.network); err != nil {
			errs = append(errs, fmt.Errorf("remove network %s: %w", j.network, err))
		}
	}
	if j.configDir != "" {
		if err := os.RemoveAll(j.configDir); err != nil {
			errs = append(errs, fmt.Errorf("remove config dir: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Create validates, persists and provisions a new project. On any provisioning failure all
// created Docker resources are removed and the database record is deleted.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (View, error) {
	proj, err := m.buildProject(req)
	if err != nil {
		return View{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return View{}, err
	}
	if _, err := m.engine.Ping(ctx); err != nil {
		return View{}, err
	}

	// Port allocation and insert happen under one lock so two concurrent creates cannot
	// pick the same port.
	m.createMu.Lock()
	port, err := m.allocatePort(ctx)
	if err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	proj.HTTPPort = port
	if req.Database != nil && req.Database.ExposePort {
		if err := m.assignDatabasePort(ctx, &proj, port); err != nil {
			m.createMu.Unlock()
			return View{}, err
		}
	}
	if err := m.store.Projects.Create(ctx, &proj); err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	m.createMu.Unlock()

	unlock, err := m.lock(proj.ID)
	if err != nil {
		return View{}, err
	}
	defer unlock()

	plan, err := planner.Plan(proj)
	if err != nil {
		_ = m.store.Projects.Delete(ctx, proj.ID)
		return View{}, err
	}

	j := journal{configDir: plan.ConfigDir}
	fail := func(step string, cause error) (View, error) {
		m.log.Error("project creation failed, rolling back", "project", proj.Slug, "step", step, "err", cause)
		rbErr := m.rollback(ctx, j)
		if delErr := m.store.Projects.Delete(context.WithoutCancel(ctx), proj.ID); delErr != nil {
			rbErr = errors.Join(rbErr, delErr)
		}
		m.audit.Log(ctx, audit.ActionProjectFailed, "project", proj.ID, map[string]any{"name": proj.Name, "step": step, "error": cause.Error()})
		if rbErr != nil {
			return View{}, fmt.Errorf("%s: %w (rollback incomplete: %v)", step, cause, rbErr)
		}
		return View{}, fmt.Errorf("%s: %w", step, cause)
	}

	// A repository is cloned into the empty directory; the starter page would collide.
	if err := m.ensureProjectDir(planner, proj, req.CreateStarter && proj.Git.URL == ""); err != nil {
		return fail("prepare project directory", err)
	}
	if proj.Git.URL != "" {
		if _, err := m.clone(ctx, proj); err != nil {
			return fail("clone repository", err)
		}
	}
	if err := writePlanFiles(plan); err != nil {
		return fail("write configuration", err)
	}
	for _, img := range plan.Images {
		if err := m.engine.EnsureImage(ctx, img, m.pullProgress(proj.Slug)); err != nil {
			return fail("pull image "+img, err)
		}
	}
	if _, err := m.engine.CreateNetwork(ctx, plan.NetworkName, plan.Labels); err != nil {
		return fail("create network", err)
	}
	j.network = plan.NetworkName
	for _, v := range plan.Volumes {
		if err := m.engine.CreateVolume(ctx, v, plan.Labels); err != nil {
			return fail("create volume "+v, err)
		}
		j.volumes = append(j.volumes, v)
	}
	for _, c := range plan.Containers {
		id, err := m.engine.CreateContainer(ctx, c.Spec)
		if err != nil {
			return fail("create container "+c.Spec.Name, err)
		}
		j.containers = append(j.containers, id)
	}
	if req.Start {
		for _, id := range j.containers {
			if err := m.engine.StartContainer(ctx, id); err != nil {
				return fail("start container", err)
			}
		}
	}
	if err := m.store.Projects.UpdateState(ctx, proj.ID, proj.DesiredState, store.LifecycleReady, ""); err != nil {
		return fail("finalise project", err)
	}
	m.audit.Log(ctx, audit.ActionProjectCreated, "project", proj.ID, map[string]any{
		"name": proj.Name, "slug": proj.Slug, "path": proj.Path, "port": proj.HTTPPort, "services": serviceSummary(proj),
	})
	return m.Get(ctx, proj.ID)
}

func serviceSummary(p store.Project) []string {
	var out []string
	for _, s := range p.Services {
		if s.Enabled {
			out = append(out, fmt.Sprintf("%s:%s", s.Kind, s.Version))
		}
	}
	return out
}

func (m *Manager) pullProgress(slug string) docker.PullProgress {
	return func(msg string) { m.log.Info("image pull", "project", slug, "status", msg) }
}

// Start brings all project containers up, recreating missing ones from the plan.
func (m *Manager) Start(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, audit.ActionProjectStarted, func(ctx context.Context, proj store.Project, plan Plan) error {
		return m.startPlan(ctx, proj, plan)
	}, store.DesiredRunning)
}

// Stop stops all project containers.
func (m *Manager) Stop(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, audit.ActionProjectStopped, func(ctx context.Context, proj store.Project, plan Plan) error {
		return m.stopPlan(ctx, proj, plan)
	}, store.DesiredStopped)
}

// Restart stops and starts the project, re-applying generated configuration. It also pulls
// the runtime images so rebuilt upstream images (PHP patch releases) are picked up;
// containers whose image changed are recreated.
func (m *Manager) Restart(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, audit.ActionProjectRestarted, func(ctx context.Context, proj store.Project, plan Plan) error {
		for _, img := range plan.Images {
			if err := m.engine.PullImage(ctx, img, m.pullProgress(proj.Slug)); err != nil {
				// A registry hiccup must not prevent a restart with the local image.
				m.log.Warn("image refresh failed, using local image", "image", img, "err", err)
			}
		}
		if err := m.stopPlan(ctx, proj, plan); err != nil {
			return err
		}
		return m.startPlan(ctx, proj, plan)
	}, store.DesiredRunning)
}

func (m *Manager) transition(ctx context.Context, id, action string, op func(context.Context, store.Project, Plan) error, desired store.DesiredState) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()

	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	if proj.Lifecycle == store.LifecycleDeleting {
		return View{}, fmt.Errorf("%w: project is being deleted", ErrConflict)
	}
	planner, err := m.planner()
	if err != nil {
		return View{}, err
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return View{}, err
	}
	if err := op(ctx, proj, plan); err != nil {
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
		return View{}, err
	}
	if err := m.store.Projects.UpdateState(ctx, id, desired, store.LifecycleReady, ""); err != nil {
		return View{}, err
	}
	m.audit.Log(ctx, action, "project", id, map[string]any{"name": proj.Name})
	return m.Get(ctx, id)
}

// startPlan ensures config files, network and containers exist and starts them in order.
func (m *Manager) startPlan(ctx context.Context, proj store.Project, plan Plan) error {
	return m.ensurePlan(ctx, proj, plan, true)
}

// ensurePlan writes config files, ensures the network and all planned containers exist
// (recreating containers whose image changed) and optionally starts them in order.
func (m *Manager) ensurePlan(ctx context.Context, proj store.Project, plan Plan, start bool) error {
	if err := writePlanFiles(plan); err != nil {
		return err
	}
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	hasNet := false
	for _, n := range networks {
		if n.Name == plan.NetworkName {
			hasNet = true
			break
		}
	}
	if !hasNet {
		if _, err := m.engine.CreateNetwork(ctx, plan.NetworkName, plan.Labels); err != nil {
			return fmt.Errorf("create network: %w", err)
		}
	}
	if len(plan.Volumes) > 0 {
		volumes, err := m.engine.ListVolumes(ctx, true)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, v := range volumes {
			have[v.Name] = true
		}
		for _, v := range plan.Volumes {
			if !have[v] {
				if err := m.engine.CreateVolume(ctx, v, plan.Labels); err != nil {
					return fmt.Errorf("create volume %s: %w", v, err)
				}
			}
		}
	}
	existing, err := m.engine.ListContainers(ctx, true, proj.ID)
	if err != nil {
		return err
	}
	byKind := map[string]docker.Container{}
	for _, c := range existing {
		byKind[c.Service()] = c
	}
	for _, c := range plan.Containers {
		cur, ok := byKind[string(c.Kind)]
		if ok {
			if err := m.engine.EnsureImage(ctx, c.Spec.Image, m.pullProgress(proj.Slug)); err != nil {
				return fmt.Errorf("pull image %s: %w", c.Spec.Image, err)
			}
			localID, err := m.engine.ImageID(ctx, c.Spec.Image)
			if err != nil {
				return fmt.Errorf("inspect image %s: %w", c.Spec.Image, err)
			}
			if cur.Image != c.Spec.Image || (cur.ImageID != "" && cur.ImageID != localID) {
				// Runtime version changed or the image tag was rebuilt upstream: recreate.
				m.log.Info("recreating container with updated image", "container", cur.Name, "from", cur.Image, "to", c.Spec.Image)
				if err := m.engine.RemoveContainer(ctx, cur.ID); err != nil {
					return fmt.Errorf("remove outdated container %s: %w", cur.Name, err)
				}
				ok = false
			}
		}
		id := cur.ID
		if !ok {
			if err := m.engine.EnsureImage(ctx, c.Spec.Image, m.pullProgress(proj.Slug)); err != nil {
				return fmt.Errorf("pull image %s: %w", c.Spec.Image, err)
			}
			id, err = m.engine.CreateContainer(ctx, c.Spec)
			if err != nil {
				return fmt.Errorf("create container %s: %w", c.Spec.Name, err)
			}
		}
		if start && (cur.State != "running" || !ok) {
			if err := m.engine.StartContainer(ctx, id); err != nil {
				return fmt.Errorf("start container %s: %w", c.Spec.Name, err)
			}
		}
	}
	return nil
}

// stopPlan stops existing containers in reverse start order.
func (m *Manager) stopPlan(ctx context.Context, proj store.Project, plan Plan) error {
	existing, err := m.engine.ListContainers(ctx, true, proj.ID)
	if err != nil {
		return err
	}
	byKind := map[string]docker.Container{}
	for _, c := range existing {
		byKind[c.Service()] = c
	}
	var errs []error
	for i := len(plan.Containers) - 1; i >= 0; i-- {
		c, ok := byKind[string(plan.Containers[i].Kind)]
		if !ok || c.State != "running" {
			continue
		}
		if err := m.engine.StopContainer(ctx, c.ID, m.cfg.StopTimeout); err != nil {
			errs = append(errs, fmt.Errorf("stop container %s: %w", c.Name, err))
		}
	}
	// Stop containers of kinds no longer in the plan as well (e.g. a removed service).
	for kind, c := range byKind {
		if plan.Container(store.ServiceKind(kind)) == nil && c.State == "running" {
			if err := m.engine.StopContainer(ctx, c.ID, m.cfg.StopTimeout); err != nil {
				errs = append(errs, fmt.Errorf("stop container %s: %w", c.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Update changes project settings and, if the project is running, restarts it so the new
// configuration takes effect.
func (m *Manager) Update(ctx context.Context, id string, req UpdateRequest) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()

	proj, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if proj.Lifecycle != store.LifecycleReady {
		return View{}, fmt.Errorf("%w: project is not in a ready state", ErrConflict)
	}
	changes := map[string]any{}

	name, docroot := proj.Name, proj.Docroot
	if req.Name != nil {
		if err := validate.ProjectName(*req.Name); err != nil {
			return View{}, err
		}
		name = *req.Name
		changes["name"] = name
	}
	if req.Docroot != nil {
		d, err := validate.OptionalRelativePath(*req.Docroot, 4)
		if err != nil {
			return View{}, fmt.Errorf("document root: %w", err)
		}
		docroot = d
		changes["docroot"] = d
	}
	if name != proj.Name || docroot != proj.Docroot {
		if err := m.store.Projects.UpdateSettings(ctx, id, name, docroot); err != nil {
			return View{}, err
		}
	}
	if req.PHP != nil {
		v, err := m.catalog.Resolve("php", req.PHP.Version)
		if err != nil {
			return View{}, err
		}
		cfg := req.PHP.Config
		if err := cfg.Normalize(); err != nil {
			return View{}, err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return View{}, err
		}
		if proj.Service(store.ServicePHP) == nil {
			return View{}, fmt.Errorf("%w: project has no PHP service", ErrConflict)
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, id, store.ServicePHP, v.Version, v.Image, raw); err != nil {
			return View{}, err
		}
		changes["php"] = v.Version
	}
	if req.Env != nil {
		env, err := buildEnv(*req.Env)
		if err != nil {
			return View{}, err
		}
		if err := m.store.Projects.ReplaceEnv(ctx, id, env); err != nil {
			return View{}, err
		}
		changes["env"] = len(env)
	}
	recreateApp := req.Env != nil
	if req.Database != nil {
		r, err := m.applyDatabaseUpdate(ctx, proj, *req.Database, changes)
		if err != nil {
			return View{}, err
		}
		recreateApp = recreateApp || r
	}

	proj, err = m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return View{}, err
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return View{}, err
	}
	if err := m.ensureProjectDir(planner, proj, false); err != nil {
		return View{}, err
	}
	if err := writePlanFiles(plan); err != nil {
		return View{}, err
	}
	running := proj.DesiredState == store.DesiredRunning
	// Env changes (user variables, database credentials) are baked into the application
	// containers: remove them so they are recreated from the new plan (started again only
	// if the project should be running). The database container keeps running.
	if recreateApp {
		existing, err := m.engine.ListContainers(ctx, true, proj.ID)
		if err != nil {
			return View{}, err
		}
		for _, c := range existing {
			if c.Service() == string(store.ServiceDatabase) {
				continue
			}
			if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
				return View{}, fmt.Errorf("recreate container %s: %w", c.Name, err)
			}
		}
	} else if running {
		// Config file changes need a restart to take effect.
		if err := m.stopPlan(ctx, proj, plan); err != nil {
			return View{}, err
		}
	}
	if err := m.ensurePlan(ctx, proj, plan, running); err != nil {
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
		return View{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": proj.Name, "changes": changes})
	return m.Get(ctx, id)
}

// Delete removes all Docker resources and the database record. Project files are only
// removed when explicitly requested.
func (m *Manager) Delete(ctx context.Context, id string, opts DeleteOptions) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	proj, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return err
	}
	if opts.Confirm != proj.Slug {
		return fmt.Errorf("%w: confirmation must equal the project identifier %q", validate.ErrInvalid, proj.Slug)
	}
	if err := m.store.Projects.UpdateState(ctx, id, proj.DesiredState, store.LifecycleDeleting, ""); err != nil {
		return err
	}
	fail := func(step string, cause error) error {
		msg := fmt.Sprintf("%s: %v", step, cause)
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, store.LifecycleFailed, msg)
		m.audit.Log(ctx, audit.ActionProjectFailed, "project", id, map[string]any{"name": proj.Name, "step": step, "error": cause.Error()})
		return fmt.Errorf("%s: %w", step, cause)
	}

	containers, err := m.engine.ListContainers(ctx, true, id)
	if err != nil {
		return fail("list containers", err)
	}
	for _, c := range containers {
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			return fail("remove container "+c.Name, err)
		}
	}
	volumes, err := m.engine.ListVolumes(ctx, true)
	if err != nil {
		return fail("list volumes", err)
	}
	for _, v := range volumes {
		if v.Labels[docker.LabelProjectID] != id {
			continue
		}
		if err := m.engine.RemoveVolume(ctx, v.Name); err != nil {
			return fail("remove volume "+v.Name, err)
		}
	}
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return fail("list networks", err)
	}
	for _, n := range networks {
		if n.Labels[docker.LabelProjectID] != id {
			continue
		}
		if err := m.engine.RemoveNetwork(ctx, n.ID); err != nil {
			return fail("remove network "+n.Name, err)
		}
	}

	planner, err := m.planner()
	if err == nil {
		if err := os.RemoveAll(planner.ProjectConfigDir(id)); err != nil {
			return fail("remove configuration", err)
		}
		if opts.DeleteFiles {
			if err := m.removeProjectFiles(planner, proj); err != nil {
				return fail("remove project files", err)
			}
		}
	} else if opts.DeleteFiles {
		return fail("remove project files", err)
	}

	if err := m.store.Projects.Delete(ctx, id); err != nil {
		return fail("delete record", err)
	}
	m.audit.Log(ctx, audit.ActionProjectDeleted, "project", id, map[string]any{"name": proj.Name, "slug": proj.Slug, "deletedFiles": opts.DeleteFiles})
	return nil
}

// removeProjectFiles deletes the project directory after verifying it resolves inside the
// projects root and is not the root itself.
func (m *Manager) removeProjectFiles(planner *Planner, proj store.Project) error {
	root := planner.paths.ProjectsDir
	dir, err := validate.ResolveUnder(root, proj.Path)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if real == realRoot || !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("%w: refusing to delete %s", validate.ErrInvalid, dir)
	}
	return os.RemoveAll(dir)
}

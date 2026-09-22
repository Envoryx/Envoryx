package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
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
		_ = m.detachProxy(ctx, j.network)
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
// created Docker resources are removed and the database record is deleted. The work runs
// detached from ctx's cancellation (see Manager.run).
func (m *Manager) Create(ctx context.Context, req CreateRequest) (View, error) {
	return m.runView(ctx, limitProvision, Operation{Action: "create", ProjectSlug: validate.Slugify(req.Name), ProjectName: req.Name}, func(ctx context.Context) (View, error) {
		return m.create(ctx, req)
	})
}

func (m *Manager) create(ctx context.Context, req CreateRequest) (View, error) {
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

	// The project lock is taken before the row exists so the reconciler never sees an
	// unlocked project in "creating".
	unlock, err := m.lock(proj.ID)
	if err != nil {
		return View{}, err
	}
	defer unlock()

	// Port allocation and insert happen under one lock so two concurrent creates cannot
	// pick the same port.
	m.createMu.Lock()
	port, err := m.allocatePort(ctx)
	if err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	proj.HTTPPort = port
	if err := m.assignServicePorts(ctx, &proj, req); err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	if err := m.store.Projects.Create(ctx, &proj); err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	m.createMu.Unlock()
	setProject(ctx, proj.ID)

	plan, err := planner.Plan(proj)
	if err != nil {
		_ = m.store.Projects.Delete(ctx, proj.ID)
		return View{}, err
	}

	j := journal{configDir: plan.ConfigDir}
	fail := func(step string, cause error) (View, error) {
		cause = opError(ctx, cause)
		m.log.Error("project creation failed, rolling back", "project", proj.Slug, "step", step, "err", cause)
		rbErr := m.rollback(ctx, j)
		if delErr := m.store.Projects.Delete(context.WithoutCancel(ctx), proj.ID); delErr != nil {
			rbErr = errors.Join(rbErr, delErr)
		}
		m.audit.Log(ctx, audit.ActionProjectFailed, "project", proj.ID, map[string]any{"name": proj.Name, "step": step, "error": cause.Error()})
		m.notify(ctx, notify.Event{Kind: "project.failed", Level: notify.Error, Project: proj.Name, Title: fmt.Sprintf("Creating %s failed", proj.Name), Message: fmt.Sprintf("Step %q failed: %v. The project was rolled back.", step, cause)})
		if rbErr != nil {
			return View{}, fmt.Errorf("%s: %w (rollback incomplete: %v)", step, cause, rbErr)
		}
		return View{}, fmt.Errorf("%s: %w", step, cause)
	}

	// A repository or template fills the empty directory; the starter page would collide.
	// While a Node dev server serves the app nothing serves the docroot, so no starter.
	scaffold := proj.Git.URL != "" || req.Template != ""
	starter := req.CreateStarter && !scaffold
	if _, ok := nodeServesApp(proj); ok {
		starter = false
	}
	step(ctx, "Preparing the project directory")
	if err := m.ensureProjectDir(planner, proj, starter, scaffold); err != nil {
		return fail("prepare project directory", err)
	}
	if proj.Git.URL != "" {
		step(ctx, "Cloning {{url}}", "url", proj.Git.URL)
		if _, err := m.clone(ctx, proj); err != nil {
			return fail("clone repository", err)
		}
	} else if req.Template != "" {
		tpl, _ := TemplateByID(req.Template)
		step(ctx, "Scaffolding the {{template}} template", "template", tpl.Name)
		if err := m.applyTemplate(ctx, proj, tpl); err != nil {
			return fail("apply template "+tpl.ID, err)
		}
	}
	step(ctx, "Writing the configuration")
	if err := writePlanFiles(plan); err != nil {
		return fail("write configuration", err)
	}
	for _, img := range plan.Images {
		if err := m.engine.EnsureImage(ctx, img, m.pullProgress(ctx, proj.Slug, img)); err != nil {
			return fail("pull image "+img, err)
		}
	}
	step(ctx, "Creating the network {{name}}", "name", plan.NetworkName)
	if _, err := m.engine.CreateNetwork(ctx, plan.NetworkName, plan.Labels); err != nil {
		return fail("create network", err)
	}
	j.network = plan.NetworkName
	if err := m.attachProxy(ctx, plan.NetworkName); err != nil {
		return fail("attach proxy", err)
	}
	for _, v := range plan.Volumes {
		step(ctx, "Creating the volume {{name}}", "name", v)
		if err := m.engine.CreateVolume(ctx, v, plan.Labels); err != nil {
			return fail("create volume "+v, err)
		}
		j.volumes = append(j.volumes, v)
	}
	for _, c := range plan.Containers {
		step(ctx, "Creating the container {{name}}", "name", c.Spec.Name)
		id, err := m.engine.CreateContainer(ctx, c.Spec)
		if err != nil {
			return fail("create container "+c.Spec.Name, err)
		}
		j.containers = append(j.containers, id)
	}
	if req.Start {
		for i, id := range j.containers {
			step(ctx, "Starting the container {{name}}", "name", plan.Containers[i].Spec.Name)
			if err := m.engine.StartContainer(ctx, id); err != nil {
				return fail("start container", err)
			}
		}
		if _, cfg, err := storageConfig(proj); err == nil {
			step(ctx, "Setting up the object storage bucket")
			if err := m.provisionBucket(ctx, proj, cfg); err != nil {
				return fail("initialise", err)
			}
		}
	}
	step(ctx, "Finishing up")
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

// pullProgress reports an image pull to the log and as the current step.
func (m *Manager) pullProgress(ctx context.Context, slug, image string) docker.PullProgress {
	return func(msg string) {
		m.log.Info("image pull", "project", slug, "image", image, "status", msg)
		step(ctx, "Pulling the image {{image}}: {{status}}", "image", image, "status", msg)
	}
}

// Start brings all project containers up, recreating missing ones from the plan.
func (m *Manager) Start(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, limitProvision, "start", audit.ActionProjectStarted, func(ctx context.Context, proj store.Project, plan Plan) error {
		return m.startPlan(ctx, proj, plan)
	}, store.DesiredRunning)
}

// Stop stops all project containers.
func (m *Manager) Stop(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, limitStop, "stop", audit.ActionProjectStopped, func(ctx context.Context, proj store.Project, plan Plan) error {
		return m.stopPlan(ctx, proj, plan)
	}, store.DesiredStopped)
}

// Restart stops and starts the project, re-applying generated configuration. It also pulls
// the runtime images so rebuilt upstream images (PHP patch releases) are picked up;
// containers whose image changed are recreated.
func (m *Manager) Restart(ctx context.Context, id string) (View, error) {
	return m.transition(ctx, id, limitProvision, "restart", audit.ActionProjectRestarted, func(ctx context.Context, proj store.Project, plan Plan) error {
		for _, img := range plan.Images {
			if err := m.engine.PullImage(ctx, img, m.pullProgress(ctx, proj.Slug, img)); err != nil {
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

// transition runs a lifecycle step under the project lock, detached from the caller's
// cancellation, and records the outcome: the desired state on success, the error on
// failure (a shutdown or time limit is stored as its readable cause).
func (m *Manager) transition(ctx context.Context, id string, limit time.Duration, kind, action string, op func(context.Context, store.Project, Plan) error, desired store.DesiredState) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	return m.runView(ctx, limit, Operation{Action: kind, ProjectID: id}, func(ctx context.Context) (View, error) {
		return m.transitionLocked(ctx, id, action, op, desired)
	})
}

func (m *Manager) transitionLocked(ctx context.Context, id, action string, op func(context.Context, store.Project, Plan) error, desired store.DesiredState) (View, error) {
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
		err = opError(ctx, err)
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
		return View{}, err
	}
	// The Docker side is done; record it even if the operation was cancelled meanwhile.
	ctx = context.WithoutCancel(ctx)
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
		step(ctx, "Creating the network {{name}}", "name", plan.NetworkName)
		if _, err := m.engine.CreateNetwork(ctx, plan.NetworkName, plan.Labels); err != nil {
			return fmt.Errorf("create network: %w", err)
		}
	}
	if err := m.attachProxy(ctx, plan.NetworkName); err != nil {
		m.log.Warn("proxy attach failed", "network", plan.NetworkName, "err", err)
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
				step(ctx, "Creating the volume {{name}}", "name", v)
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
		// A rollback pins the containers of an image reference to the previous image id;
		// Docker accepts the id wherever a tag goes.
		tag := c.Spec.Image
		if rec := proj.ImageRecord(tag); rec != nil && rec.Pinned && rec.PreviousID != "" {
			c.Spec.Image = rec.PreviousID
		}
		cur, ok := byKind[string(c.Kind)]
		if ok {
			if err := m.engine.EnsureImage(ctx, c.Spec.Image, m.pullProgress(ctx, proj.Slug, c.Spec.Image)); err != nil {
				return fmt.Errorf("pull image %s: %w", c.Spec.Image, err)
			}
			localID, err := m.engine.ImageID(ctx, c.Spec.Image)
			if err != nil {
				return fmt.Errorf("inspect image %s: %w", c.Spec.Image, err)
			}
			specChanged := cur.Labels[docker.LabelSpec] != c.Spec.Labels[docker.LabelSpec]
			if cur.Image != c.Spec.Image || (cur.ImageID != "" && cur.ImageID != localID) || specChanged {
				// Runtime version changed, the image tag was rebuilt upstream, or the
				// container's command/mounts/ports differ from the plan: recreate.
				m.log.Info("recreating container", "container", cur.Name, "from", cur.Image, "to", c.Spec.Image, "spec_changed", specChanged)
				step(ctx, "Recreating the container {{name}}", "name", cur.Name)
				// Record (and tag) the rollback target while the old container still
				// references its image: the containerd image store garbage-collects an
				// untagged image the moment its last container is gone.
				if c.Spec.Image == tag {
					m.recordImageChange(ctx, &proj, tag, cur, localID)
				}
				if err := m.engine.RemoveContainer(ctx, cur.ID); err != nil {
					return fmt.Errorf("remove outdated container %s: %w", cur.Name, err)
				}
				ok = false
			}
		}
		id := cur.ID
		if !ok {
			if err := m.engine.EnsureImage(ctx, c.Spec.Image, m.pullProgress(ctx, proj.Slug, c.Spec.Image)); err != nil {
				return fmt.Errorf("pull image %s: %w", c.Spec.Image, err)
			}
			step(ctx, "Creating the container {{name}}", "name", c.Spec.Name)
			id, err = m.engine.CreateContainer(ctx, c.Spec)
			if err != nil {
				return fmt.Errorf("create container %s: %w", c.Spec.Name, err)
			}
		}
		if start && (cur.State != "running" || !ok) {
			step(ctx, "Starting the container {{name}}", "name", c.Spec.Name)
			if err := m.engine.StartContainer(ctx, id); err != nil {
				return fmt.Errorf("start container %s: %w", c.Spec.Name, err)
			}
		}
		if start && c.Spec.User != "" {
			m.ensurePasswdEntry(ctx, id, c.Spec.Name, c.Spec.User)
		}
	}
	if start {
		if _, cfg, err := storageConfig(proj); err == nil {
			step(ctx, "Setting up the object storage bucket")
			if err := m.provisionBucket(ctx, proj, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensurePasswdEntry gives the project uid/gid a name inside the container. The images
// know nothing about the host's PUID/PGID, and tools such as ssh, whoami, git and the
// JetBrains launcher misbehave for a uid without a passwd entry. Best effort: failures
// are logged, never fatal.
func (m *Manager) ensurePasswdEntry(ctx context.Context, containerID, name, user string) {
	uid, gid, ok := strings.Cut(user, ":")
	if !ok || uid == "0" {
		return
	}
	script := fmt.Sprintf(`getent group %[2]s >/dev/null || echo "envoryx:x:%[2]s:" >> /etc/group; `+
		`getent passwd %[1]s >/dev/null || echo "envoryx:x:%[1]s:%[2]s:Envoryx:%[3]s:/bin/sh" >> /etc/passwd`, uid, gid, homeMountTarget)
	var stderr strings.Builder
	code, err := m.engine.ExecStream(ctx, containerID, docker.ExecStreamOptions{Cmd: []string{"sh", "-c", script}, User: "0:0", Stderr: &stderr})
	if err != nil || code != 0 {
		m.log.Debug("passwd entry not created", "container", name, "exit", code, "err", err, "stderr", strings.TrimSpace(stderr.String()))
	}
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
		step(ctx, "Stopping the container {{name}}", "name", c.Name)
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
	return m.runView(ctx, limitProvision, Operation{Action: "update", ProjectID: id}, func(ctx context.Context) (View, error) {
		return m.update(ctx, id, req)
	})
}

func (m *Manager) update(ctx context.Context, id string, req UpdateRequest) (View, error) {
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
	if req.Web != nil {
		cur := proj.Service(store.ServiceWeb)
		if cur == nil {
			return View{}, fmt.Errorf("%w: project has no web service", ErrConflict)
		}
		wcfg, err := webServiceConfig(*cur)
		if err != nil {
			return View{}, err
		}
		if req.Web.SPAFallback != nil {
			if php := proj.Service(store.ServicePHP); php != nil {
				return View{}, fmt.Errorf("%w: SPA fallback needs a project without PHP; the front controller handles unknown paths", validate.ErrInvalid)
			}
			if wcfg.SPAFallback != *req.Web.SPAFallback {
				wcfg.SPAFallback = *req.Web.SPAFallback
				changes["spaFallback"] = wcfg.SPAFallback
			}
		}
		web, err := m.buildWebService(req.Web.Type, req.Web.Version, wcfg.SPAFallback)
		if err != nil {
			return View{}, err
		}
		if cur.Variant != web.Variant || cur.Version != web.Version {
			// UpdateServiceVariant resets the config; the options are web-server neutral
			// and are written back right after.
			if err := m.store.Projects.UpdateServiceVariant(ctx, id, store.ServiceWeb, web.Variant, web.Version, web.Image); err != nil {
				return View{}, err
			}
			changes["web"] = web.Variant + " " + web.Version
			if err := m.store.Projects.UpdateServiceConfig(ctx, id, store.ServiceWeb, web.Version, web.Image, web.Config); err != nil {
				return View{}, err
			}
		} else if _, ok := changes["spaFallback"]; ok {
			if err := m.store.Projects.UpdateServiceConfig(ctx, id, store.ServiceWeb, cur.Version, cur.Image, web.Config); err != nil {
				return View{}, err
			}
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
	if req.IDEGateway != nil && *req.IDEGateway != proj.IDEGateway {
		if err := m.store.Projects.SetIDEGateway(ctx, id, *req.IDEGateway); err != nil {
			return View{}, err
		}
		changes["ideGateway"] = *req.IDEGateway
	}
	if req.Node != nil {
		if err := m.applyNodeUpdate(ctx, proj, *req.Node, changes); err != nil {
			return View{}, err
		}
	}
	if req.Database != nil {
		r, err := m.applyDatabaseUpdate(ctx, proj, *req.Database, changes)
		if err != nil {
			return View{}, err
		}
		recreateApp = recreateApp || r
	}
	if req.Redis != nil {
		r, err := m.applyExtraUpdate(ctx, proj, store.ServiceRedis, *req.Redis, changes)
		if err != nil {
			return View{}, err
		}
		recreateApp = recreateApp || r
	}
	if req.Mailpit != nil {
		r, err := m.applyExtraUpdate(ctx, proj, store.ServiceMailpit, *req.Mailpit, changes)
		if err != nil {
			return View{}, err
		}
		recreateApp = recreateApp || r
	}
	if req.Storage != nil {
		r, err := m.applyStorageUpdate(ctx, proj, *req.Storage, changes)
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
	if err := m.ensureProjectDir(planner, proj, false, false); err != nil {
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
			switch c.Service() {
			case string(store.ServiceDatabase), string(store.ServiceRedis), string(store.ServiceMailpit), string(store.ServiceStorage):
				continue // stateful/independent services keep running
			}
			step(ctx, "Removing the container {{name}} so it is recreated with the new settings", "name", c.Name)
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

// applyNodeUpdate adds, changes or removes the Node.js service. Callers hold the lock.
func (m *Manager) applyNodeUpdate(ctx context.Context, p store.Project, upd NodeUpdate, changes map[string]any) error {
	svc := p.Service(store.ServiceNode)
	switch {
	case !upd.Enabled && svc == nil:
		return nil
	case !upd.Enabled:
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return err
		}
		for _, c := range containers {
			if c.Service() == string(store.ServiceNode) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return fmt.Errorf("remove node container: %w", err)
				}
			}
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, store.ServiceNode); err != nil {
			return err
		}
		changes["node"] = "removed"
		return nil
	default:
		v, err := m.catalog.Resolve("node", upd.Version)
		if err != nil {
			return err
		}
		cfg := upd.Config
		if err := cfg.Normalize(); err != nil {
			return err
		}
		var old runtime.NodeConfig
		if svc != nil && len(svc.Config) > 0 {
			_ = json.Unmarshal(svc.Config, &old)
		}
		// Keep the published port across edits; allocate one when the dev server is enabled.
		cfg.HostPort = 0
		if cfg.DevServer {
			cfg.HostPort = old.HostPort
			if cfg.HostPort == 0 {
				port, err := m.allocatePort(ctx)
				if err != nil {
					return err
				}
				cfg.HostPort = port
			}
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if svc == nil {
			if err := m.store.Projects.AddService(ctx, store.ProjectService{ProjectID: p.ID, Kind: store.ServiceNode, Variant: "node", Version: v.Version, Image: v.Image, Enabled: true, Position: 15, Config: raw}); err != nil {
				return err
			}
			changes["node"] = v.Version
			return nil
		}
		if svc.Version != v.Version || string(svc.Config) != string(raw) {
			if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, store.ServiceNode, v.Version, v.Image, raw); err != nil {
				return err
			}
			changes["node"] = v.Version
			if string(svc.Config) != string(raw) {
				changes["nodeDevServer"] = cfg.DevServer
				// Command/ports are baked into the container: remove it so ensurePlan recreates it.
				containers, err := m.engine.ListContainers(ctx, true, p.ID)
				if err != nil {
					return err
				}
				for _, c := range containers {
					if c.Service() == string(store.ServiceNode) {
						if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
							return fmt.Errorf("recreate node container: %w", err)
						}
					}
				}
			}
		}
		return nil
	}
}

// Delete removes all Docker resources and the database record. Project files are only
// removed when explicitly requested.
func (m *Manager) Delete(ctx context.Context, id string, opts DeleteOptions) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	return m.run(ctx, limitDelete, Operation{Action: "delete", ProjectID: id}, func(ctx context.Context) error { return m.delete(ctx, id, opts) })
}

func (m *Manager) delete(ctx context.Context, id string, opts DeleteOptions) error {
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
	// Refuse before touching anything: a foreign container on a project network (e.g.
	// attached through Unraid's network dropdown) would make the network removal fail
	// after the project's own containers and volumes are already gone.
	if err := m.checkForeignEndpoints(ctx, id); err != nil {
		return err
	}
	if err := m.store.Projects.UpdateState(ctx, id, proj.DesiredState, store.LifecycleDeleting, ""); err != nil {
		return err
	}
	fail := func(step string, cause error) error {
		cause = opError(ctx, cause)
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
		step(ctx, "Removing the container {{name}}", "name", c.Name)
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
		step(ctx, "Removing the volume {{name}}", "name", v.Name)
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
		step(ctx, "Removing the network {{name}}", "name", n.Name)
		if err := m.detachProxy(ctx, n.Name); err != nil {
			return fail("detach proxy from "+n.Name, err)
		}
		if err := m.detachDBTool(ctx, n.Name); err != nil {
			return fail("detach database browser from "+n.Name, err)
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
			step(ctx, "Removing the project files")
			if err := m.removeProjectFiles(planner, proj); err != nil {
				return fail("remove project files", err)
			}
		}
	} else if opts.DeleteFiles {
		return fail("remove project files", err)
	}

	if history, err := m.store.Images.ListByProject(ctx, id); err == nil {
		for _, rec := range history {
			m.releaseRollbackTarget(ctx, proj.Slug, rec.Image)
		}
	}
	if err := m.store.Projects.Delete(ctx, id); err != nil {
		return fail("delete record", err)
	}
	m.audit.Log(ctx, audit.ActionProjectDeleted, "project", id, map[string]any{"name": proj.Name, "slug": proj.Slug, "deletedFiles": opts.DeleteFiles})
	return nil
}

// checkForeignEndpoints returns ErrConflict naming every container on the project's
// networks that Envoryx did not put there (project containers, its own proxy and the
// database browser are expected and detached during delete).
func (m *Manager) checkForeignEndpoints(ctx context.Context, id string) error {
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	own := map[string]bool{}
	containers, err := m.engine.ListContainers(ctx, true, id)
	if err != nil {
		return err
	}
	for _, c := range containers {
		own[c.ID] = true
	}
	if paths, err := m.paths(); err == nil && paths.SelfContainerID != "" {
		own[paths.SelfContainerID] = true
	}
	if c, err := m.findDBTool(ctx); err == nil && c != nil {
		own[c.ID] = true
	}
	isOwn := func(containerID string) bool {
		for o := range own {
			if strings.HasPrefix(containerID, o) || strings.HasPrefix(o, containerID) {
				return true
			}
		}
		return false
	}
	for _, n := range networks {
		if n.Labels[docker.LabelProjectID] != id {
			continue
		}
		endpoints, err := m.engine.NetworkEndpoints(ctx, n.Name)
		if err != nil {
			return fmt.Errorf("inspect network %s: %w", n.Name, err)
		}
		var foreign []string
		for _, ep := range endpoints {
			if !isOwn(ep.ContainerID) {
				foreign = append(foreign, ep.Name)
			}
		}
		if len(foreign) > 0 {
			return fmt.Errorf("%w: network %s is still used by %s – disconnect or remove %s first",
				ErrConflict, n.Name, strings.Join(foreign, ", "), plural(len(foreign), "that container", "these containers"))
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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

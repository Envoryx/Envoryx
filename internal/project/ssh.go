package project

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ExecTarget is everything an SSH session needs to run commands inside an application
// container as the project owner and to map SFTP paths.
type ExecTarget struct {
	Project     store.Project
	Kind        store.ServiceKind
	ContainerID string
	Running     bool
	User        string
	Env         []string
	WorkingDir  string
	// ProjectDir / HomeDir are the Envoryx-side directories behind /var/www/html and
	// /home/envoryx (used by the SFTP subsystem).
	ProjectDir string
	HomeDir    string
	// AppMount / HomeMount are the paths inside the container.
	AppMount, HomeMount string
	// Mounts maps every bind mount visible to the SSH user (container path → Envoryx-side
	// directory), including the shared JetBrains cache when Gateway is enabled, so SFTP
	// shows the same tree as a shell in the container.
	Mounts map[string]string
	// ContainerName resolves on the project network (used for SSH port forwarding).
	ContainerName string
	// Gateway is true when port forwarding into the container is allowed.
	Gateway bool
}

// StopIDEBackend kills JetBrains IDE backend processes (started by Gateway) in the
// project's application containers. It runs pkill as the project owner, so only the
// project's own processes are affected.
func (m *Manager) StopIDEBackend(ctx context.Context, id string) (int, error) {
	if err := validate.UUID(id); err != nil {
		return 0, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return 0, err
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, c := range containers {
		if c.State != "running" || (c.Service() != string(store.ServicePHP) && c.Service() != string(store.ServiceNode)) {
			continue
		}
		res, err := m.engine.Exec(ctx, c.ID, []string{"pkill", "-f", "/.cache/JetBrains/"}, []string{"HOME=" + homeMountTarget})
		if err != nil {
			return stopped, err
		}
		if res.ExitCode == 0 {
			stopped++
		}
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"ideBackendStopped": stopped}})
	return stopped, nil
}

// ResolveSSHUser maps an SSH user name to a project and application container:
// "<slug>" → the project's application container (PHP, or Node when there is no PHP);
// "<slug>.php" / "<slug>.node" select explicitly.
func (m *Manager) ResolveSSHUser(ctx context.Context, user string) (ExecTarget, error) {
	slug, kind := user, store.ServiceKind("")
	if s, ok := strings.CutSuffix(user, ".php"); ok {
		slug, kind = s, store.ServicePHP
	} else if s, ok := strings.CutSuffix(user, ".node"); ok {
		slug, kind = s, store.ServiceNode
	}
	if err := validate.Slug(slug); err != nil {
		return ExecTarget{}, fmt.Errorf("%w: unknown user", store.ErrNotFound)
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return ExecTarget{}, err
	}
	for _, p := range projects {
		if p.Slug != slug {
			continue
		}
		var svc *store.ProjectService
		if kind == "" {
			if svc = appService(p); svc == nil {
				return ExecTarget{}, fmt.Errorf("%w: project %s has no application container", store.ErrNotFound, slug)
			}
			kind = svc.Kind
		} else if svc = p.Service(kind); svc == nil || !svc.Enabled {
			return ExecTarget{}, fmt.Errorf("%w: project %s has no %s service", store.ErrNotFound, slug, kind)
		}
		paths, err := m.paths()
		if err != nil {
			return ExecTarget{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
		}
		planner := NewPlanner(paths, m.catalog)
		t := ExecTarget{
			Project: p, Kind: kind, User: fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
			Env:        append([]string{"LANG=C.UTF-8", "TERM=xterm-256color"}, toolEnv...),
			WorkingDir: appMountTarget, ProjectDir: planner.ProjectDir(p), HomeDir: planner.HomeDir(p),
			AppMount: appMountTarget, HomeMount: homeMountTarget,
			ContainerName: ContainerName(p.Slug, kind), Gateway: p.IDEGateway,
		}
		t.Mounts = map[string]string{t.AppMount: t.ProjectDir, t.HomeMount: t.HomeDir}
		if p.IDEGateway {
			t.Mounts[homeMountTarget+"/.cache/JetBrains"] = filepath.Join(paths.ConfigDir, jetbrainsCacheDir)
		}
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return ExecTarget{}, err
		}
		for _, c := range containers {
			if c.Service() == string(kind) {
				t.ContainerID, t.Running = c.ID, c.State == "running"
			}
		}
		return t, nil
	}
	return ExecTarget{}, fmt.Errorf("%w: unknown user", store.ErrNotFound)
}

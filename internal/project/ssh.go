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
		if c.State != "running" || !isAppKind(store.ServiceKind(c.Service())) {
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

// isAppKind reports whether a service kind is an application container (PHP, Python, Go,
// Ruby, Node): the containers that run as the project owner with the project home mounted.
func isAppKind(kind store.ServiceKind) bool {
	return kind == store.ServicePHP || kind == store.ServicePython || kind == store.ServiceGo || kind == store.ServiceRuby || kind == store.ServiceNode
}

// ResolveSSHUser maps an SSH user name to a project and application container:
// "<slug>" → the project's application container (PHP, else Python, else Go, else Ruby,
// else Node); "<slug>.php" / "<slug>.python" / "<slug>.go" / "<slug>.ruby" / "<slug>.node"
// select explicitly.
func (m *Manager) ResolveSSHUser(ctx context.Context, user string) (ExecTarget, error) {
	slug, kind := user, store.ServiceKind("")
	for _, k := range []store.ServiceKind{store.ServicePHP, store.ServicePython, store.ServiceGo, store.ServiceRuby, store.ServiceNode} {
		if s, ok := strings.CutSuffix(user, "."+string(k)); ok {
			slug, kind = s, k
			break
		}
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
			// SHELL as OpenSSH sets it from passwd: IDEs run their probes as `$SHELL -l -c …`
			// (PyCharm's SSH interpreter failed with "-l: not found" without it).
			Env:        append([]string{"LANG=C.UTF-8", "TERM=xterm-256color", "SHELL=/bin/sh"}, toolEnv...),
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

// CallbackAddresses tells where a process in the target container can reach Envoryx and
// which address such a connection arrives from: Envoryx's address on the project network
// (its own container's, or on bare metal the network's gateway, which is the host) and
// the container's address there. SSH remote forwarding (ssh -R) listens on the first and
// accepts only the second.
func (m *Manager) CallbackAddresses(ctx context.Context, t ExecTarget) (envoryx, container string, err error) {
	paths, err := m.paths()
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	gateway, ips, err := m.engine.NetworkAddresses(ctx, NetworkName(t.Project.Slug))
	if err != nil {
		return "", "", err
	}
	container = ips[t.ContainerID]
	envoryx = gateway
	if self := paths.SelfContainerID; self != "" {
		envoryx = ""
		for id, ip := range ips {
			if strings.HasPrefix(id, self) {
				envoryx = ip
			}
		}
	}
	if container == "" || envoryx == "" {
		return "", "", fmt.Errorf("no address on %s for %s or Envoryx", NetworkName(t.Project.Slug), t.ContainerName)
	}
	return envoryx, container, nil
}

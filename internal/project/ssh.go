package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
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
	// ProjectDir / HomeDir are the Staqio-side directories behind /var/www/html and
	// /home/staqio (used by the SFTP subsystem).
	ProjectDir string
	HomeDir    string
	// AppMount / HomeMount are the paths inside the container.
	AppMount, HomeMount string
}

// ResolveSSHUser maps an SSH user name to a project and application container:
// "<slug>" → PHP, "<slug>.node" → Node.
func (m *Manager) ResolveSSHUser(ctx context.Context, user string) (ExecTarget, error) {
	slug, kind := user, store.ServicePHP
	if s, ok := strings.CutSuffix(user, ".node"); ok {
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
		svc := p.Service(kind)
		if svc == nil || !svc.Enabled {
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

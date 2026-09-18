package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ServiceContainer resolves the container of a project service. The browser never
// addresses containers by id; this lookup is the only path from (project, kind) to a
// container and it only ever returns containers labelled for that project.
func (m *Manager) ServiceContainer(ctx context.Context, id string, kind store.ServiceKind) (docker.Container, error) {
	if err := validate.UUID(id); err != nil {
		return docker.Container{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return docker.Container{}, err
	}
	if wid, ok := strings.CutPrefix(string(kind), "worker:"); ok {
		found := false
		for _, w := range p.Workers {
			found = found || (w.ID == wid && w.Enabled)
		}
		if !found {
			return docker.Container{}, fmt.Errorf("%w: project has no such worker", store.ErrNotFound)
		}
	} else if svc := p.Service(kind); svc == nil || !svc.Enabled {
		return docker.Container{}, fmt.Errorf("%w: project has no %s service", store.ErrNotFound, kind)
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return docker.Container{}, err
	}
	for _, c := range containers {
		if c.Service() == string(kind) {
			return c, nil
		}
	}
	return docker.Container{}, fmt.Errorf("%w: the %s container does not exist; start the project", store.ErrNotFound, kind)
}

// StreamLogs streams the logs of a project service until ctx is cancelled.
func (m *Manager) StreamLogs(ctx context.Context, id string, kind store.ServiceKind, opts docker.LogOptions, emit func(docker.LogLine)) error {
	c, err := m.ServiceContainer(ctx, id, kind)
	if err != nil {
		return err
	}
	return m.engine.StreamLogs(ctx, c.ID, opts, emit)
}

// TailLogs returns the last n lines of a project service.
func (m *Manager) TailLogs(ctx context.Context, id string, kind store.ServiceKind, n int) ([]docker.LogLine, error) {
	if n <= 0 || n > 10000 {
		n = 500
	}
	lines := make([]docker.LogLine, 0, 256)
	err := m.StreamLogs(ctx, id, kind, docker.LogOptions{Tail: fmt.Sprint(n)}, func(l docker.LogLine) {
		lines = append(lines, l)
	})
	if err != nil {
		return nil, err
	}
	return lines, nil
}

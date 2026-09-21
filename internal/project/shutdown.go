package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
)

// SettingProjectsFollowEnvoryx is the settings-table key for the opt-in that ties the
// project containers to the Envoryx container: they are stopped when Envoryx stops and
// started again when it comes back.
const SettingProjectsFollowEnvoryx = "projects_follow_envoryx"

// resumeParallelism bounds how many projects are started at once after a restart.
const resumeParallelism = 3

// ProjectsFollowEnvoryx reports whether project containers follow the Envoryx container.
// Off by default: the containers carry `unless-stopped` and outlive an Envoryx update.
func (m *Manager) ProjectsFollowEnvoryx(ctx context.Context) bool {
	v, err := m.store.Settings.Get(ctx, SettingProjectsFollowEnvoryx)
	return err == nil && v == "true"
}

// SetProjectsFollowEnvoryx stores the preference.
func (m *Manager) SetProjectsFollowEnvoryx(ctx context.Context, on bool) error {
	if err := m.store.Settings.Set(ctx, SettingProjectsFollowEnvoryx, strconv.FormatBool(on)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"projectsFollowEnvoryx": on})
	return nil
}

// StopAllForShutdown stops the running containers of every project, then every other
// managed container that is still running (the database browser, orphans). Projects are
// stopped in parallel, each in the order of its plan; no project's desired state changes –
// this is Envoryx going down, not the user stopping the project, so ResumeProjects can
// bring them back. It is meant to run after Shutdown has drained the lifecycle
// operations; a project whose lock is still held is skipped.
func (m *Manager) StopAllForShutdown(ctx context.Context) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		m.log.Warn("stop projects at shutdown: load projects", "err", err)
	}
	planner, err := m.planner()
	if err != nil {
		m.log.Warn("stop projects at shutdown: planner unavailable", "err", err)
	}
	var wg sync.WaitGroup
	if planner != nil {
		for _, p := range projects {
			unlock, err := m.lock(p.ID)
			if err != nil {
				m.log.Warn("stop projects at shutdown: project busy, left running", "project", p.Name)
				continue
			}
			plan, err := planner.Plan(p)
			if err != nil {
				unlock()
				m.log.Warn("stop projects at shutdown: plan", "project", p.Name, "err", err)
				continue
			}
			wg.Add(1)
			go func(p store.Project, plan Plan) {
				defer wg.Done()
				defer unlock()
				if err := m.stopPlan(ctx, p, plan); err != nil {
					m.log.Warn("stop projects at shutdown", "project", p.Name, "err", err)
				}
			}(p, plan)
		}
	}
	wg.Wait()

	// Whatever managed container still runs is not part of a stoppable project.
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		m.log.Warn("stop projects at shutdown: list containers", "err", err)
		return
	}
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		go func(c docker.Container) {
			defer wg.Done()
			if err := m.engine.StopContainer(ctx, c.ID, m.cfg.StopTimeout); err != nil {
				m.log.Warn("stop container at shutdown", "container", c.Name, "err", err)
			}
		}(c)
	}
	wg.Wait()
}

// ResumeProjects starts every project the user wants running that is not: the projects
// StopAllForShutdown took down, and those a host reboot left stopped (a container stopped
// explicitly is not restarted by `unless-stopped`). Projects in a transitional or failed
// lifecycle are left to the reconciler. Failures are recorded on the project like any
// other start; a shutdown meanwhile interrupts the starts through Manager.run.
func (m *Manager) ResumeProjects(ctx context.Context, log *slog.Logger) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		log.Warn("resume projects: load projects", "err", err)
		return
	}
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		log.Warn("resume projects: docker unavailable", "err", err)
		return
	}
	var pending []store.Project
	for _, p := range projects {
		if p.DesiredState != store.DesiredRunning || p.Lifecycle != store.LifecycleReady {
			continue
		}
		if deriveStatus(p, containers, nil).State != StateRunning {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return
	}
	log.Info("resuming projects that were running before the restart", "count", len(pending))
	begin := time.Now()
	sem := make(chan struct{}, resumeParallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var resumed []string
	for _, p := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(p store.Project) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := m.Start(ctx, p.ID); err != nil {
				if errors.Is(err, ErrShuttingDown) || errors.Is(err, ErrInterrupted) {
					return
				}
				// The failure is on the project (last error) and in the reconcile issues.
				log.Warn("resume project", "project", p.Name, "err", err)
				return
			}
			mu.Lock()
			resumed = append(resumed, p.Name)
			mu.Unlock()
			log.Info("project resumed", "project", p.Name)
		}(p)
	}
	wg.Wait()
	log.Info("projects resumed", "count", len(resumed), "of", len(pending), "took", time.Since(begin).Round(time.Millisecond))
	if len(resumed) > 0 {
		sort.Strings(resumed)
		m.recordActivity(ActivityProjectsResumed, resumed, "Projects resumed",
			fmt.Sprintf("Envoryx is back and started %d %s again: %s.", len(resumed), plural(len(resumed), "project", "projects"), strings.Join(resumed, ", ")))
	}
}

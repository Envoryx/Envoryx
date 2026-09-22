package project

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// WorkerPreset is a closed catalogue entry describing a long-running command.
type WorkerPreset struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// ArgLabel/ArgHint describe the optional user argument (empty = none).
	ArgLabel string `json:"argLabel,omitempty"`
	ArgHint  string `json:"argHint,omitempty"`
	// Requires lists files that must exist for the preset to make sense (informational).
	Requires []string `json:"requires,omitempty"`
	// Runtime is the service the worker runs in: "php" (default), "node" or "python".
	Runtime string `json:"runtime"`

	// build returns argv for the validated argument.
	build func(arg string) []string
	// validateArg checks the argument (nil = no argument accepted).
	validateArg func(arg string) error
}

var (
	queueNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}(,[A-Za-z0-9_.-]{1,64})*$`)
	scriptNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)
	workerNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
)

func queueArg(arg string) error {
	if arg == "" {
		return nil
	}
	if !queueNameRe.MatchString(arg) {
		return fmt.Errorf("%w: queue/transport names may contain letters, digits, '.', '_' and '-' (comma separated)", validate.ErrInvalid)
	}
	return nil
}

// Worker runtimes.
const (
	WorkerRuntimePHP    = "php"
	WorkerRuntimeNode   = "node"
	WorkerRuntimePython = "python"
)

// workerRuntimeKind maps a preset runtime to the service it runs in.
func workerRuntimeKind(rt string) (store.ServiceKind, string) {
	switch rt {
	case WorkerRuntimeNode:
		return store.ServiceNode, "Node.js"
	case WorkerRuntimePython:
		return store.ServicePython, "Python"
	default:
		return store.ServicePHP, "PHP"
	}
}

// scriptPathArg validates a project-relative script path argument.
func scriptPathArg(arg string) error {
	if arg == "" {
		return fmt.Errorf("%w: script path is required", validate.ErrInvalid)
	}
	_, err := validate.RelativePath(arg, 6)
	return err
}

var workerPresets = []WorkerPreset{
	{ID: "laravel:schedule", Group: "Laravel", Label: "Scheduler", Description: "php artisan schedule:work – runs the scheduled tasks every minute", Requires: []string{"artisan"},
		build: func(string) []string { return []string{"php", "artisan", "schedule:work"} }},
	{ID: "laravel:queue", Group: "Laravel", Label: "Queue worker", Description: "php artisan queue:work – processes jobs (restart after code changes)", ArgLabel: "Queues", ArgHint: "e.g. default,emails (empty = default)", Requires: []string{"artisan"},
		validateArg: queueArg,
		build: func(arg string) []string {
			cmd := []string{"php", "artisan", "queue:work", "--tries=3", "--sleep=3", "--max-time=3600"}
			if arg != "" {
				cmd = append(cmd, "--queue="+arg)
			}
			return cmd
		}},
	{ID: "laravel:queue-listen", Group: "Laravel", Label: "Queue listener", Description: "php artisan queue:listen – slower, but picks up code changes automatically", ArgLabel: "Queues", ArgHint: "e.g. default,emails", Requires: []string{"artisan"},
		validateArg: queueArg,
		build: func(arg string) []string {
			cmd := []string{"php", "artisan", "queue:listen", "--tries=3", "--sleep=3"}
			if arg != "" {
				cmd = append(cmd, "--queue="+arg)
			}
			return cmd
		}},
	{ID: "laravel:horizon", Group: "Laravel", Label: "Horizon", Description: "php artisan horizon – Redis queue supervisor", Requires: []string{"artisan"},
		build: func(string) []string { return []string{"php", "artisan", "horizon"} }},
	{ID: "laravel:reverb", Group: "Laravel", Label: "Reverb", Description: "php artisan reverb:start – WebSocket server on port 8080 inside the project network", Requires: []string{"artisan"},
		build: func(string) []string {
			return []string{"php", "artisan", "reverb:start", "--host=0.0.0.0", "--port=8080"}
		}},
	{ID: "symfony:messenger", Group: "Symfony", Label: "Messenger consumer", Description: "bin/console messenger:consume – processes transports (restarts hourly)", ArgLabel: "Transports", ArgHint: "e.g. async (empty = async)", Requires: []string{"bin/console"},
		validateArg: queueArg,
		build: func(arg string) []string {
			if arg == "" {
				arg = "async"
			}
			cmd := []string{"php", "bin/console", "messenger:consume"}
			cmd = append(cmd, strings.Split(arg, ",")...)
			return append(cmd, "--time-limit=3600", "-vv")
		}},
	{ID: "symfony:scheduler", Group: "Symfony", Label: "Scheduler", Description: "bin/console messenger:consume scheduler_default – Symfony Scheduler", ArgLabel: "Schedule name", ArgHint: "empty = default", Requires: []string{"bin/console"},
		validateArg: func(arg string) error {
			if arg != "" && !scriptNameRe.MatchString(arg) {
				return fmt.Errorf("%w: invalid schedule name", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string {
			if arg == "" {
				arg = "default"
			}
			return []string{"php", "bin/console", "messenger:consume", "scheduler_" + arg, "--time-limit=3600", "-vv"}
		}},
	{ID: "php:script", Group: "PHP", Label: "PHP script", Description: "php <file> – any long-running script in the project directory", ArgLabel: "Script path", ArgHint: "relative to the project, e.g. bin/worker.php",
		validateArg: func(arg string) error {
			if arg == "" {
				return fmt.Errorf("%w: script path is required", validate.ErrInvalid)
			}
			_, err := validate.RelativePath(arg, 6)
			return err
		},
		build: func(arg string) []string { p, _ := validate.RelativePath(arg, 6); return []string{"php", p} }},
	{ID: "composer:script", Group: "Composer", Label: "Composer script", Description: "composer run-script <name> – a script from composer.json", ArgLabel: "Script name", ArgHint: "e.g. worker", Requires: []string{"composer.json"},
		validateArg: func(arg string) error {
			if !scriptNameRe.MatchString(arg) {
				return fmt.Errorf("%w: invalid composer script name", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"composer", "run-script", "--no-interaction", "--", arg} }},
	{ID: "node:script", Group: "Node.js", Label: "npm script", Description: "npm run <name> – a long-running script from package.json (queue consumer, scheduler, bot …)", ArgLabel: "Script name", ArgHint: "e.g. worker", Requires: []string{"package.json"}, Runtime: WorkerRuntimeNode,
		validateArg: func(arg string) error {
			if !scriptNameRe.MatchString(arg) {
				return fmt.Errorf("%w: invalid npm script name", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"npm", "run", arg} }},
	{ID: "node:file", Group: "Node.js", Label: "Node.js script", Description: "node <file> – any long-running script in the project directory", ArgLabel: "Script path", ArgHint: "relative to the project, e.g. workers/queue.js", Runtime: WorkerRuntimeNode,
		validateArg: func(arg string) error {
			if arg == "" {
				return fmt.Errorf("%w: script path is required", validate.ErrInvalid)
			}
			_, err := validate.RelativePath(arg, 6)
			return err
		},
		build: func(arg string) []string { p, _ := validate.RelativePath(arg, 6); return []string{"node", p} }},
	{ID: "python:file", Group: "Python", Label: "Python script", Description: "python <file> – any long-running script in the project directory (runs in the project's .venv)", ArgLabel: "Script path", ArgHint: "relative to the project, e.g. workers/consume.py", Runtime: WorkerRuntimePython,
		validateArg: scriptPathArg,
		build:       func(arg string) []string { p, _ := validate.RelativePath(arg, 6); return []string{"python", p} }},
	{ID: "python:module", Group: "Python", Label: "Python module", Description: "python -m <module> – a long-running module (queue consumer, scheduler, bot …)", ArgLabel: "Module", ArgHint: "e.g. app.worker", Runtime: WorkerRuntimePython,
		validateArg: func(arg string) error {
			if !runtime.ValidAppPath(arg) || strings.Contains(arg, ":") {
				return fmt.Errorf("%w: invalid module path", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"python", "-m", arg} }},
	{ID: "django:command", Group: "Django", Label: "manage.py command", Description: "python manage.py <command> – a long-running management command (rqworker, qcluster, run_huey …)", ArgLabel: "Command", ArgHint: "e.g. rqworker", Requires: []string{"manage.py"}, Runtime: WorkerRuntimePython,
		validateArg: func(arg string) error {
			if !scriptNameRe.MatchString(arg) {
				return fmt.Errorf("%w: invalid management command name", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"python", "manage.py", arg} }},
	{ID: "celery:worker", Group: "Celery", Label: "Celery worker", Description: "celery -A <app> worker – processes tasks (Redis or RabbitMQ broker)", ArgLabel: "Celery app", ArgHint: "e.g. config or proj.celery", Runtime: WorkerRuntimePython,
		validateArg: func(arg string) error {
			if !runtime.ValidAppPath(arg) {
				return fmt.Errorf("%w: invalid Celery app path", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"celery", "-A", arg, "worker", "--loglevel=info"} }},
	{ID: "celery:beat", Group: "Celery", Label: "Celery beat", Description: "celery -A <app> beat – the periodic task scheduler", ArgLabel: "Celery app", ArgHint: "e.g. config or proj.celery", Runtime: WorkerRuntimePython,
		validateArg: func(arg string) error {
			if !runtime.ValidAppPath(arg) {
				return fmt.Errorf("%w: invalid Celery app path", validate.ErrInvalid)
			}
			return nil
		},
		build: func(arg string) []string { return []string{"celery", "-A", arg, "beat", "--loglevel=info"} }},
}

func init() {
	for i := range workerPresets {
		if workerPresets[i].Runtime == "" {
			workerPresets[i].Runtime = WorkerRuntimePHP
		}
	}
}

// workerRuntimeService returns the enabled service a stored worker runs in, nil when the
// project lacks it (unknown presets count as PHP, like the catalogue default).
func workerRuntimeService(p store.Project, w store.Worker) *store.ProjectService {
	kind := store.ServicePHP
	if preset, ok := workerPreset(w.Preset); ok {
		kind, _ = workerRuntimeKind(preset.Runtime)
	}
	if svc := p.Service(kind); svc != nil && svc.Enabled {
		return svc
	}
	return nil
}

// workerRuntimeAvailable checks that the project has the service a preset runs in.
func workerRuntimeAvailable(p store.Project, preset WorkerPreset) error {
	kind, label := workerRuntimeKind(preset.Runtime)
	if svc := p.Service(kind); svc == nil || !svc.Enabled {
		return fmt.Errorf("%w: the %s preset runs from the %s image – this project has no %s service", ErrConflict, preset.Label, label, label)
	}
	return nil
}

// WorkerPresets lists the catalogue.
func WorkerPresets() []WorkerPreset {
	out := make([]WorkerPreset, len(workerPresets))
	copy(out, workerPresets)
	return out
}

func workerPreset(id string) (WorkerPreset, bool) {
	for _, p := range workerPresets {
		if p.ID == id {
			return p, true
		}
	}
	return WorkerPreset{}, false
}

// WorkerCommand returns the argv of a stored worker (validated again defensively).
func WorkerCommand(w store.Worker) ([]string, error) {
	p, ok := workerPreset(w.Preset)
	if !ok {
		return nil, fmt.Errorf("%w: unknown worker preset %q", validate.ErrInvalid, w.Preset)
	}
	arg := ""
	if len(w.Args) > 0 {
		arg = w.Args[0]
	}
	if p.validateArg != nil {
		if err := p.validateArg(arg); err != nil {
			return nil, err
		}
	} else if arg != "" {
		return nil, fmt.Errorf("%w: preset %s takes no argument", validate.ErrInvalid, p.ID)
	}
	return p.build(arg), nil
}

// WorkerKind is the service label of a worker container ("worker:<id>").
func WorkerKind(w store.Worker) store.ServiceKind { return store.ServiceKind("worker:" + w.ID) }

// WorkerContainerName is the container name of a worker.
func WorkerContainerName(slug string, w store.Worker) string {
	return "envoryx-" + slug + "-worker-" + w.Name
}

// WorkerRequest is the API-facing worker definition.
type WorkerRequest struct {
	Name    string
	Preset  string
	Arg     string
	Enabled bool
}

func buildWorker(projectID string, req WorkerRequest) (store.Worker, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !workerNameRe.MatchString(name) {
		return store.Worker{}, fmt.Errorf("%w: worker name must be 1-32 lower-case letters, digits or hyphens", validate.ErrInvalid)
	}
	w := store.Worker{ProjectID: projectID, Name: name, Preset: strings.TrimSpace(req.Preset), Enabled: req.Enabled, Args: []string{}}
	if arg := strings.TrimSpace(req.Arg); arg != "" {
		w.Args = []string{arg}
	}
	if _, err := WorkerCommand(w); err != nil {
		return store.Worker{}, err
	}
	return w, nil
}

// AddWorker creates a worker and applies the plan.
func (m *Manager) AddWorker(ctx context.Context, id string, req WorkerRequest) (store.Worker, error) {
	if err := validate.UUID(id); err != nil {
		return store.Worker{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return store.Worker{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return store.Worker{}, err
	}
	if len(p.Workers) >= 10 {
		return store.Worker{}, fmt.Errorf("%w: at most 10 workers per project", validate.ErrInvalid)
	}
	w, err := buildWorker(id, req)
	if err != nil {
		return store.Worker{}, err
	}
	if preset, ok := workerPreset(w.Preset); ok {
		if err := workerRuntimeAvailable(p, preset); err != nil {
			return store.Worker{}, err
		}
	}
	w.Position = len(p.Workers)
	if err := m.store.Workers.Add(ctx, &w); err != nil {
		return store.Worker{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"workerAdded": w.Name, "preset": w.Preset}})
	return w, m.applyWorkers(ctx, id)
}

// UpdateWorker changes a worker (name, preset, argument, enabled) and applies the plan.
func (m *Manager) UpdateWorker(ctx context.Context, id, workerID string, req WorkerRequest) (store.Worker, error) {
	if err := validate.UUID(id); err != nil {
		return store.Worker{}, ErrNotFound
	}
	if err := validate.UUID(workerID); err != nil {
		return store.Worker{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return store.Worker{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return store.Worker{}, err
	}
	old, err := m.store.Workers.Get(ctx, id, workerID)
	if err != nil {
		return store.Worker{}, err
	}
	w, err := buildWorker(id, req)
	if err != nil {
		return store.Worker{}, err
	}
	if preset, ok := workerPreset(w.Preset); ok && w.Enabled {
		if err := workerRuntimeAvailable(p, preset); err != nil {
			return store.Worker{}, err
		}
	}
	w.ID, w.Position, w.CreatedAt = old.ID, old.Position, old.CreatedAt
	if err := m.store.Workers.Update(ctx, w); err != nil {
		return store.Worker{}, err
	}
	// Command/name are baked into the container: remove it so ensurePlan recreates it.
	if old.Name != w.Name || old.Preset != w.Preset || strings.Join(old.Args, ",") != strings.Join(w.Args, ",") || (!w.Enabled && old.Enabled) {
		if err := m.removeWorkerContainer(ctx, id, old); err != nil {
			return store.Worker{}, err
		}
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"workerUpdated": w.Name, "enabled": w.Enabled}})
	return w, m.applyWorkers(ctx, id)
}

// RemoveWorker deletes a worker and its container.
func (m *Manager) RemoveWorker(ctx context.Context, id, workerID string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := validate.UUID(workerID); err != nil {
		return ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	w, err := m.store.Workers.Get(ctx, id, workerID)
	if err != nil {
		return err
	}
	if err := m.removeWorkerContainer(ctx, id, w); err != nil {
		return err
	}
	if err := m.store.Workers.Delete(ctx, id, workerID); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"workerRemoved": w.Name}})
	return nil
}

func (m *Manager) removeWorkerContainer(ctx context.Context, projectID string, w store.Worker) error {
	containers, err := m.engine.ListContainers(ctx, true, projectID)
	if err != nil {
		return err
	}
	for _, c := range containers {
		if c.Service() == string(WorkerKind(w)) {
			if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
				return fmt.Errorf("remove worker container: %w", err)
			}
		}
	}
	return nil
}

// applyWorkers re-plans the project so worker containers match the stored definitions.
// Callers hold the project lock.
func (m *Manager) applyWorkers(ctx context.Context, id string) error {
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	planner, err := m.planner()
	if err != nil {
		return err
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return err
	}
	if proj.Lifecycle != store.LifecycleReady {
		return nil
	}
	return m.ensurePlan(ctx, proj, plan, proj.DesiredState == store.DesiredRunning)
}

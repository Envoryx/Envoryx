package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/cron"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Cron jobs are commands Envoryx runs on a schedule inside a project's application
// container, the way `docker exec` would: as the project owner in the project directory,
// through `sh -c` so pipes and `&&` work, bounded by coreutils' timeout. There is no cron
// daemon in the images; the scheduler lives in Envoryx and only fires while the project
// is meant to be running. Runs missed while Envoryx or the project was down are not
// caught up.
const (
	maxCronJobs         = 20
	cronRunsKept        = 20
	cronOutputLimit     = 64 << 10
	cronCommandLimit    = 4000
	cronDefaultTimeout  = 10 * time.Minute
	cronMaxTimeout      = 24 * time.Hour
	cronMaxParallelRuns = 8
	// cronKillGrace is how long timeout waits after SIGTERM before it sends SIGKILL.
	cronKillGrace = 10 * time.Second
)

// CronJobRequest is the API-facing cron job definition.
type CronJobRequest struct {
	Name     string
	Runtime  string
	Schedule string
	Command  string
	// Timeout bounds one run; 0 means the default (10 minutes).
	Timeout time.Duration
	Enabled bool
}

// CronJobInfo is a cron job with what the UI shows next to it.
type CronJobInfo struct {
	store.CronJob
	// NextRun is the next scheduled time (nil when disabled or the runtime is missing).
	NextRun *time.Time
	// Running is true while a run is in progress.
	Running bool
	// RuntimeMissing is true when the project has no service of the job's runtime; the
	// job then does not run.
	RuntimeMissing bool
}

// cronState is the runner's in-memory bookkeeping.
type cronState struct {
	mu      sync.Mutex
	running map[string]bool // job id -> a run is in progress
	sem     chan struct{}
	wg      sync.WaitGroup
}

func (m *Manager) cron() *cronState {
	m.cronOnce.Do(func() {
		m.cronRuns = &cronState{running: map[string]bool{}, sem: make(chan struct{}, cronMaxParallelRuns)}
	})
	return m.cronRuns
}

func buildCronJob(projectID string, req CronJobRequest) (store.CronJob, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !workerNameRe.MatchString(name) {
		return store.CronJob{}, fmt.Errorf("%w: cron job name must be 1-32 lower-case letters, digits or hyphens", validate.ErrInvalid)
	}
	switch req.Runtime {
	case WorkerRuntimePHP, WorkerRuntimeNode, WorkerRuntimePython, WorkerRuntimeGo:
	default:
		return store.CronJob{}, fmt.Errorf("%w: runtime must be php, node, python or go", validate.ErrInvalid)
	}
	sched, err := cron.Parse(req.Schedule)
	if err != nil {
		return store.CronJob{}, fmt.Errorf("%w: %v", validate.ErrInvalid, err)
	}
	// Pasted from a Windows editor: CRLF line ends become LF, a lone CR stays an error.
	command := strings.TrimSpace(strings.ReplaceAll(req.Command, "\r\n", "\n"))
	if command == "" || len(command) > cronCommandLimit {
		return store.CronJob{}, fmt.Errorf("%w: the command must be 1-%d characters", validate.ErrInvalid, cronCommandLimit)
	}
	for _, r := range command {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return store.CronJob{}, fmt.Errorf("%w: the command contains a control character", validate.ErrInvalid)
		}
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = cronDefaultTimeout
	}
	if timeout < time.Second || timeout > cronMaxTimeout || timeout%time.Second != 0 {
		return store.CronJob{}, fmt.Errorf("%w: the timeout must be 1 second to 24 hours in whole seconds", validate.ErrInvalid)
	}
	return store.CronJob{ProjectID: projectID, Name: name, Runtime: req.Runtime, Schedule: sched.String(), Command: command, Timeout: timeout, Enabled: req.Enabled}, nil
}

func cronRuntimeService(p store.Project, runtime string) *store.ProjectService {
	kind, _ := workerRuntimeKind(runtime)
	if svc := p.Service(kind); svc != nil && svc.Enabled {
		return svc
	}
	return nil
}

// cronRuntimeAvailable refuses an enabled job whose runtime the project lacks.
func cronRuntimeAvailable(p store.Project, j store.CronJob) error {
	if !j.Enabled || cronRuntimeService(p, j.Runtime) != nil {
		return nil
	}
	_, label := workerRuntimeKind(j.Runtime)
	return fmt.Errorf("%w: the job runs in the %s container – this project has no %s service", ErrConflict, label, label)
}

func (m *Manager) cronInfo(p store.Project, j store.CronJob, now time.Time) CronJobInfo {
	info := CronJobInfo{CronJob: j, RuntimeMissing: cronRuntimeService(p, j.Runtime) == nil}
	st := m.cron()
	st.mu.Lock()
	info.Running = st.running[j.ID]
	st.mu.Unlock()
	if j.Enabled && !info.RuntimeMissing {
		if s, err := cron.Parse(j.Schedule); err == nil {
			if next, ok := s.Next(now); ok {
				info.NextRun = &next
			}
		}
	}
	return info
}

// CronJobs lists a project's cron jobs.
func (m *Manager) CronJobs(ctx context.Context, id string) ([]CronJobInfo, error) {
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	jobs, err := m.store.CronJobs.ListByProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]CronJobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, m.cronInfo(p, j, now))
	}
	return out, nil
}

// AddCronJob stores a new cron job.
func (m *Manager) AddCronJob(ctx context.Context, id string, req CronJobRequest) (CronJobInfo, error) {
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return CronJobInfo{}, err
	}
	jobs, err := m.store.CronJobs.ListByProject(ctx, p.ID)
	if err != nil {
		return CronJobInfo{}, err
	}
	if len(jobs) >= maxCronJobs {
		return CronJobInfo{}, fmt.Errorf("%w: at most %d cron jobs per project", validate.ErrInvalid, maxCronJobs)
	}
	j, err := buildCronJob(p.ID, req)
	if err != nil {
		return CronJobInfo{}, err
	}
	if err := cronRuntimeAvailable(p, j); err != nil {
		return CronJobInfo{}, err
	}
	j.Position = len(jobs)
	if err := m.store.CronJobs.Add(ctx, &j); err != nil {
		return CronJobInfo{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", p.ID, map[string]any{"name": p.Name, "changes": map[string]any{"cronJobAdded": j.Name, "schedule": j.Schedule, "command": truncateCmd(j.Command, 200)}})
	return m.cronInfo(p, j, time.Now()), nil
}

// UpdateCronJob replaces a cron job's definition. A run in progress finishes with the
// old command.
func (m *Manager) UpdateCronJob(ctx context.Context, id, jobID string, req CronJobRequest) (CronJobInfo, error) {
	if err := validate.UUID(jobID); err != nil {
		return CronJobInfo{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return CronJobInfo{}, err
	}
	old, err := m.store.CronJobs.Get(ctx, p.ID, jobID)
	if err != nil {
		return CronJobInfo{}, err
	}
	j, err := buildCronJob(p.ID, req)
	if err != nil {
		return CronJobInfo{}, err
	}
	if err := cronRuntimeAvailable(p, j); err != nil {
		return CronJobInfo{}, err
	}
	j.ID, j.Position, j.CreatedAt, j.LastRun = old.ID, old.Position, old.CreatedAt, old.LastRun
	if err := m.store.CronJobs.Update(ctx, j); err != nil {
		return CronJobInfo{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", p.ID, map[string]any{"name": p.Name, "changes": map[string]any{"cronJobUpdated": j.Name, "enabled": j.Enabled, "schedule": j.Schedule, "command": truncateCmd(j.Command, 200)}})
	return m.cronInfo(p, j, time.Now()), nil
}

// RemoveCronJob deletes a cron job and its run history. A run in progress is left to
// finish; its result is dropped.
func (m *Manager) RemoveCronJob(ctx context.Context, id, jobID string) error {
	if err := validate.UUID(jobID); err != nil {
		return ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	j, err := m.store.CronJobs.Get(ctx, p.ID, jobID)
	if err != nil {
		return err
	}
	if err := m.store.CronJobs.Delete(ctx, p.ID, jobID); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", p.ID, map[string]any{"name": p.Name, "changes": map[string]any{"cronJobRemoved": j.Name}})
	return nil
}

// CronRuns returns the recent runs of a job with their output, newest first.
func (m *Manager) CronRuns(ctx context.Context, id, jobID string) ([]store.CronRun, error) {
	if err := validate.UUID(jobID); err != nil {
		return nil, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := m.store.CronJobs.Get(ctx, p.ID, jobID); err != nil {
		return nil, err
	}
	return m.store.CronJobs.Runs(ctx, jobID, cronRunsKept)
}

// CronPreview validates a schedule and returns its next n fire times in Envoryx's time
// zone.
func CronPreview(expr string, n int, now time.Time) ([]time.Time, error) {
	s, err := cron.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", validate.ErrInvalid, err)
	}
	return s.NextN(now, n), nil
}

// RunCronJobNow starts a job outside its schedule (disabled jobs too) and returns the run
// that was started; it continues in the background. A job already running is refused.
func (m *Manager) RunCronJobNow(ctx context.Context, id, jobID string) (store.CronRun, error) {
	if err := validate.UUID(jobID); err != nil {
		return store.CronRun{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return store.CronRun{}, err
	}
	j, err := m.store.CronJobs.Get(ctx, p.ID, jobID)
	if err != nil {
		return store.CronRun{}, err
	}
	if cronRuntimeService(p, j.Runtime) == nil {
		_, label := workerRuntimeKind(j.Runtime)
		return store.CronRun{}, fmt.Errorf("%w: this project has no %s service", ErrConflict, label)
	}
	run, ok, err := m.startCronRun(context.WithoutCancel(ctx), p, j, store.CronSourceManual)
	if err != nil {
		return store.CronRun{}, err
	}
	if !ok {
		return store.CronRun{}, fmt.Errorf("%w: %s is still running", ErrConflict, j.Name)
	}
	m.audit.Log(ctx, audit.ActionCronRun, "project", p.ID, map[string]any{"name": p.Name, "job": j.Name, "command": truncateCmd(j.Command, 200)})
	return run, nil
}

// startCronRun records a run and executes it in the background. ok is false when the job
// is already running (the run is then skipped, not queued).
func (m *Manager) startCronRun(ctx context.Context, p store.Project, j store.CronJob, source string) (store.CronRun, bool, error) {
	st := m.cron()
	st.mu.Lock()
	if st.running[j.ID] {
		st.mu.Unlock()
		return store.CronRun{}, false, nil
	}
	st.running[j.ID] = true
	st.mu.Unlock()
	done := func() {
		st.mu.Lock()
		delete(st.running, j.ID)
		st.mu.Unlock()
	}
	run := store.CronRun{JobID: j.ID, Source: source}
	if err := m.store.CronJobs.StartRun(ctx, &run); err != nil {
		done()
		return store.CronRun{}, false, err
	}
	// The goroutine fills in its own copy; the caller gets the run as it started.
	bg := run
	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		defer done()
		st.sem <- struct{}{}
		defer func() { <-st.sem }()
		m.executeCronRun(ctx, p, j, &bg)
	}()
	return run, true, nil
}

// executeCronRun runs the command and stores the outcome.
func (m *Manager) executeCronRun(ctx context.Context, p store.Project, j store.CronJob, run *store.CronRun) {
	out := &tailBuffer{limit: cronOutputLimit}
	run.Status, run.ExitCode = m.execCron(ctx, p, j, out)
	run.Output, run.Truncated = out.String(), out.truncated
	if err := m.store.CronJobs.FinishRun(context.WithoutCancel(ctx), run, cronRunsKept); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.log.Warn("cron: storing the run failed", "project", p.Slug, "job", j.Name, "err", err)
	}
	key := "cron.failed|" + j.ID
	if run.Status == store.CronSucceeded {
		if m.notifier != nil {
			m.notifier.Clear(key)
		}
		return
	}
	m.log.Info("cron job did not succeed", "project", p.Slug, "job", j.Name, "status", run.Status, "exit", run.ExitCode)
	msg := fmt.Sprintf("%s: %s", j.Name, cronStatusText(run.Status, run.ExitCode))
	if tail := lastLines(run.Output, 5); tail != "" {
		msg += "\n\n" + tail
	}
	m.notify(ctx, notify.Event{Kind: "cron.failed", Level: notify.Error, Project: p.Name, Title: fmt.Sprintf("Cron job %s of %s failed", j.Name, p.Name), Message: msg, Key: key})
}

func cronStatusText(status string, code int) string {
	switch status {
	case store.CronTimedOut:
		return "timed out"
	case store.CronError:
		return "could not run"
	case store.CronInterrupted:
		return "interrupted"
	default:
		return "exit code " + strconv.Itoa(code)
	}
}

// execCron runs the command in the job's application container.
func (m *Manager) execCron(ctx context.Context, p store.Project, j store.CronJob, out *tailBuffer) (string, int) {
	kind, label := workerRuntimeKind(j.Runtime)
	fail := func(format string, args ...any) (string, int) {
		fmt.Fprintf(out, format+"\n", args...)
		return store.CronError, -1
	}
	c, err := m.ServiceContainer(ctx, p.ID, kind)
	if err != nil {
		return fail("The %s container was not found: %v", label, err)
	}
	if c.State != "running" {
		return fail("The %s container is %s.", label, c.State)
	}
	env, err := m.execEnv(kind)
	if err != nil {
		return fail("%v", err)
	}
	m.ensurePasswdEntry(ctx, c.ID, c.Name, env.User)
	secs := int(j.Timeout / time.Second)
	// timeout(1) ends the command inside the container; the context only guards against
	// a hanging exec.
	cmd := []string{"timeout", "-s", "TERM", "-k", strconv.Itoa(int(cronKillGrace / time.Second)), strconv.Itoa(secs), "sh", "-c", j.Command}
	runCtx, cancel := context.WithTimeout(ctx, j.Timeout+cronKillGrace+30*time.Second)
	defer cancel()
	code, err := m.engine.ExecStream(runCtx, c.ID, docker.ExecStreamOptions{
		Cmd:        cmd,
		Env:        append(env.Env, "CI=1", "ENVORYX_CRON_JOB="+j.Name),
		User:       env.User,
		WorkingDir: env.WorkingDir,
		Stdout:     out,
		Stderr:     out,
	})
	switch {
	case ctx.Err() != nil:
		// Envoryx is shutting down; the command may still finish inside the container.
		fmt.Fprintf(out, "\n[envoryx] Envoryx stopped while the job was running\n")
		return store.CronInterrupted, -1
	case runCtx.Err() != nil:
		fmt.Fprintf(out, "\n[envoryx] the run did not end within its timeout of %s\n", j.Timeout)
		return store.CronTimedOut, -1
	case err != nil:
		return fail("Running the command failed: %v", err)
	case code == 124 || code == 137:
		// 124: timeout sent SIGTERM; 137: it had to follow up with SIGKILL.
		fmt.Fprintf(out, "\n[envoryx] stopped after the timeout of %s\n", j.Timeout)
		return store.CronTimedOut, code
	case code != 0:
		return store.CronFailed, code
	}
	return store.CronSucceeded, 0
}

// RunCronScheduler fires due cron jobs until ctx ends. Each job's next time is computed
// from its schedule when the scheduler first sees it (or its schedule changes) and again
// after every run, so a minute is never fired twice – also not in the hour a DST change
// repeats.
func (m *Manager) RunCronScheduler(ctx context.Context, interval time.Duration, log *slog.Logger) {
	if n, err := m.store.CronJobs.InterruptRunning(ctx); err != nil {
		log.Warn("cron: marking interrupted runs failed", "err", err)
	} else if n > 0 {
		log.Info("cron: runs interrupted by the last shutdown", "count", n)
	}
	next := map[string]cronNext{}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		m.cronPass(ctx, time.Now(), next, log)
		select {
		case <-ctx.Done():
			m.cron().wg.Wait()
			return
		case <-t.C:
		}
	}
}

type cronNext struct {
	schedule string
	at       time.Time
}

// cronPass fires the jobs that are due at now. next carries each job's next fire time
// between passes.
func (m *Manager) cronPass(ctx context.Context, now time.Time, next map[string]cronNext, log *slog.Logger) {
	jobs, err := m.store.CronJobs.ListEnabled(ctx)
	if err != nil {
		log.Warn("cron: listing jobs failed", "err", err)
		return
	}
	projects := map[string]*store.Project{}
	seen := map[string]bool{}
	for _, j := range jobs {
		seen[j.ID] = true
		s, err := cron.Parse(j.Schedule)
		if err != nil {
			continue
		}
		n, ok := next[j.ID]
		if !ok || n.schedule != j.Schedule {
			// New to the scheduler: the current minute still counts.
			at, _ := s.Next(now.Truncate(time.Minute).Add(-time.Minute))
			n = cronNext{schedule: j.Schedule, at: at}
		}
		if n.at.IsZero() || now.Before(n.at) {
			next[j.ID] = n
			continue
		}
		n.at, _ = s.Next(now)
		next[j.ID] = n

		p, ok := projects[j.ProjectID]
		if !ok {
			loaded, err := m.store.Projects.Get(ctx, j.ProjectID)
			if err != nil {
				continue
			}
			p = &loaded
			projects[j.ProjectID] = p
		}
		// Only while the project is meant to run; a stopped project skips its jobs.
		if p.DesiredState != store.DesiredRunning || p.Lifecycle != store.LifecycleReady || cronRuntimeService(*p, j.Runtime) == nil {
			continue
		}
		if _, started, err := m.startCronRun(ctx, *p, j, store.CronSourceSchedule); err != nil {
			log.Warn("cron: starting a run failed", "project", p.Slug, "job", j.Name, "err", err)
		} else if !started {
			log.Info("cron: skipped, the previous run is still going", "project", p.Slug, "job", j.Name)
		}
	}
	for id := range next {
		if !seen[id] {
			delete(next, id)
		}
	}
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.ToValidUTF8(string(b.buf), "")
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

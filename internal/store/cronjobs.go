package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CronJob is a command run on a schedule inside a project's application container.
// Schedule, runtime and command are validated by the project package.
type CronJob struct {
	ID        string
	ProjectID string
	Name      string
	// Runtime is the application service the command runs in: php, node or python.
	Runtime  string
	Schedule string
	Command  string
	Timeout  time.Duration
	Enabled  bool
	Position int
	// LastRun is the newest run, nil before the first one (filled by ListByProject/Get).
	LastRun   *CronRun
	CreatedAt time.Time
}

// Cron run sources and states.
const (
	CronSourceSchedule = "schedule"
	CronSourceManual   = "manual"

	CronRunning     = "running"
	CronSucceeded   = "succeeded"
	CronFailed      = "failed"    // non-zero exit code
	CronTimedOut    = "timed_out" // killed after the job's timeout
	CronError       = "error"     // could not run at all (container stopped …)
	CronInterrupted = "interrupted"
)

// CronRun is one execution of a cron job.
type CronRun struct {
	ID         string
	JobID      string
	Source     string
	Status     string
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Output     string
	Truncated  bool
}

// CronJobs is the repository for cron jobs and their runs.
type CronJobs struct{ db *sql.DB }

const cronJobColumns = `id, project_id, name, runtime, schedule, command, timeout_seconds, enabled, position, created_at`

const cronRunColumns = `id, job_id, source, status, exit_code, started_at, finished_at, output, truncated`

func scanCronJob(row interface{ Scan(...any) error }) (CronJob, error) {
	var j CronJob
	var created string
	var timeout, enabled int
	if err := row.Scan(&j.ID, &j.ProjectID, &j.Name, &j.Runtime, &j.Schedule, &j.Command, &timeout, &enabled, &j.Position, &created); err != nil {
		return CronJob{}, err
	}
	j.Timeout = time.Duration(timeout) * time.Second
	j.Enabled = enabled != 0
	j.CreatedAt = parseTime(created)
	return j, nil
}

func scanCronRun(row interface{ Scan(...any) error }) (CronRun, error) {
	var r CronRun
	var started, finished string
	var truncated int
	if err := row.Scan(&r.ID, &r.JobID, &r.Source, &r.Status, &r.ExitCode, &started, &finished, &r.Output, &truncated); err != nil {
		return CronRun{}, err
	}
	r.StartedAt = parseTime(started)
	if finished != "" {
		r.FinishedAt = parseTime(finished)
	}
	r.Truncated = truncated != 0
	return r, nil
}

func (r *CronJobs) query(ctx context.Context, where string, args ...any) ([]CronJob, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+cronJobColumns+` FROM project_cron_jobs `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select cron jobs: %w", err)
	}
	defer rows.Close()
	out := []CronJob{}
	for rows.Next() {
		j, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := r.attachLastRun(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *CronJobs) attachLastRun(ctx context.Context, j *CronJob) error {
	run, err := scanCronRun(r.db.QueryRowContext(ctx, `SELECT `+cronRunColumns+` FROM project_cron_runs WHERE job_id = ? ORDER BY rowid DESC LIMIT 1`, j.ID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("select last cron run: %w", err)
	}
	run.Output = "" // the list stays small; runs come with their output from Runs
	j.LastRun = &run
	return nil
}

// ListByProject returns the cron jobs of a project in position order.
func (r *CronJobs) ListByProject(ctx context.Context, projectID string) ([]CronJob, error) {
	return r.query(ctx, `WHERE project_id = ? ORDER BY position, name`, projectID)
}

// ListEnabled returns the enabled cron jobs of all projects (for the scheduler).
func (r *CronJobs) ListEnabled(ctx context.Context) ([]CronJob, error) {
	return r.query(ctx, `WHERE enabled = 1 ORDER BY project_id, position`)
}

// Get returns one cron job of a project.
func (r *CronJobs) Get(ctx context.Context, projectID, id string) (CronJob, error) {
	j, err := scanCronJob(r.db.QueryRowContext(ctx, `SELECT `+cronJobColumns+` FROM project_cron_jobs WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CronJob{}, ErrNotFound
	}
	if err != nil {
		return CronJob{}, fmt.Errorf("select cron job: %w", err)
	}
	return j, r.attachLastRun(ctx, &j)
}

// Add stores a cron job.
func (r *CronJobs) Add(ctx context.Context, j *CronJob) error {
	if j.ID == "" {
		j.ID = NewID()
	}
	j.CreatedAt = now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_cron_jobs (`+cronJobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.ProjectID, j.Name, j.Runtime, j.Schedule, j.Command, int(j.Timeout/time.Second), boolInt(j.Enabled), j.Position, formatTime(j.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a cron job named %q already exists: %w", j.Name, ErrConflict)
		}
		return fmt.Errorf("insert cron job: %w", err)
	}
	return nil
}

// Update changes everything but the id, project and position.
func (r *CronJobs) Update(ctx context.Context, j CronJob) error {
	res, err := r.db.ExecContext(ctx, `UPDATE project_cron_jobs SET name = ?, runtime = ?, schedule = ?, command = ?, timeout_seconds = ?, enabled = ? WHERE project_id = ? AND id = ?`,
		j.Name, j.Runtime, j.Schedule, j.Command, int(j.Timeout/time.Second), boolInt(j.Enabled), j.ProjectID, j.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a cron job named %q already exists: %w", j.Name, ErrConflict)
		}
		return fmt.Errorf("update cron job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a cron job with its runs.
func (r *CronJobs) Delete(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM project_cron_jobs WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StartRun records a run that has begun.
func (r *CronJobs) StartRun(ctx context.Context, run *CronRun) error {
	if run.ID == "" {
		run.ID = NewID()
	}
	run.StartedAt, run.Status = now(), CronRunning
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_cron_runs (`+cronRunColumns+`) VALUES (?, ?, ?, ?, 0, ?, '', '', 0)`,
		run.ID, run.JobID, run.Source, run.Status, formatTime(run.StartedAt))
	if err != nil {
		return fmt.Errorf("insert cron run: %w", err)
	}
	return nil
}

// FinishRun stores the outcome of a run and drops all but the newest keep runs of its job.
func (r *CronJobs) FinishRun(ctx context.Context, run *CronRun, keep int) error {
	run.FinishedAt = now()
	if _, err := r.db.ExecContext(ctx, `UPDATE project_cron_runs SET status = ?, exit_code = ?, finished_at = ?, output = ?, truncated = ? WHERE id = ?`,
		run.Status, run.ExitCode, formatTime(run.FinishedAt), run.Output, boolInt(run.Truncated), run.ID); err != nil {
		return fmt.Errorf("update cron run: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM project_cron_runs WHERE job_id = ? AND id NOT IN
		(SELECT id FROM project_cron_runs WHERE job_id = ? ORDER BY rowid DESC LIMIT ?)`, run.JobID, run.JobID, keep); err != nil {
		return fmt.Errorf("prune cron runs: %w", err)
	}
	return nil
}

// Runs returns the runs of a job, newest first, with their output.
func (r *CronJobs) Runs(ctx context.Context, jobID string, limit int) ([]CronRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+cronRunColumns+` FROM project_cron_runs WHERE job_id = ? ORDER BY rowid DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("select cron runs: %w", err)
	}
	defer rows.Close()
	out := []CronRun{}
	for rows.Next() {
		run, err := scanCronRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// InterruptRunning marks runs left "running" by a previous process as interrupted.
func (r *CronJobs) InterruptRunning(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE project_cron_runs SET status = ?, finished_at = ? WHERE status = ?`, CronInterrupted, formatTime(now()), CronRunning)
	if err != nil {
		return 0, fmt.Errorf("interrupt cron runs: %w", err)
	}
	return res.RowsAffected()
}

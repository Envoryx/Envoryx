package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Test run states.
const (
	TestPassed    = "passed"
	TestFailed    = "failed"    // failures or a non-zero exit code
	TestCancelled = "cancelled" // stopped from the UI or the connection went away
)

// TestRun is one run of a project's test suite.
type TestRun struct {
	ID        string
	ProjectID string
	Suite     string
	Filter    string
	Status    string
	ExitCode  int
	StartedAt time.Time
	Duration  time.Duration
	// Result holds what the report said (project.TestResult as JSON).
	Result json.RawMessage
}

// TestRuns is the repository for test runs.
type TestRuns struct{ db *sql.DB }

const testRunColumns = `id, project_id, suite, filter, status, exit_code, started_at, duration_ms, result`

// Add records a run and keeps the newest keep runs of the project.
func (r *TestRuns) Add(ctx context.Context, run *TestRun, keep int) error {
	if run.ID == "" {
		run.ID = NewID()
	}
	if len(run.Result) == 0 {
		run.Result = json.RawMessage("{}")
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO project_test_runs (`+testRunColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.Suite, run.Filter, run.Status, run.ExitCode, formatTime(run.StartedAt), run.Duration.Milliseconds(), string(run.Result)); err != nil {
		return fmt.Errorf("insert test run: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM project_test_runs WHERE project_id = ? AND id NOT IN
		(SELECT id FROM project_test_runs WHERE project_id = ? ORDER BY started_at DESC LIMIT ?)`, run.ProjectID, run.ProjectID, keep); err != nil {
		return fmt.Errorf("prune test runs: %w", err)
	}
	return nil
}

// List returns the runs of a project, newest first.
func (r *TestRuns) List(ctx context.Context, projectID string, limit int) ([]TestRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+testRunColumns+` FROM project_test_runs WHERE project_id = ? ORDER BY started_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestRun
	for rows.Next() {
		run, err := scanTestRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Get returns one run of a project.
func (r *TestRuns) Get(ctx context.Context, projectID, id string) (TestRun, error) {
	run, err := scanTestRun(r.db.QueryRowContext(ctx, `SELECT `+testRunColumns+` FROM project_test_runs WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TestRun{}, ErrNotFound
	}
	return run, err
}

func scanTestRun(s interface{ Scan(...any) error }) (TestRun, error) {
	var run TestRun
	var started, result string
	var ms int64
	if err := s.Scan(&run.ID, &run.ProjectID, &run.Suite, &run.Filter, &run.Status, &run.ExitCode, &started, &ms, &result); err != nil {
		return TestRun{}, err
	}
	run.StartedAt = parseTime(started)
	run.Duration = time.Duration(ms) * time.Millisecond
	run.Result = json.RawMessage(result)
	return run, nil
}

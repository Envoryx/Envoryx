package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Worker is a long-running process of a project (queue worker, scheduler) run in its own
// container from the PHP image. Preset + Args are validated by the project package.
type Worker struct {
	ID        string
	ProjectID string
	Name      string
	Preset    string
	Args      []string
	Enabled   bool
	Position  int
	CreatedAt time.Time
}

// Workers is the repository for project workers.
type Workers struct{ db *sql.DB }

const workerColumns = `id, project_id, name, preset, args, enabled, position, created_at`

func scanWorker(row interface{ Scan(...any) error }) (Worker, error) {
	var w Worker
	var args, created string
	var enabled int
	if err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Preset, &args, &enabled, &w.Position, &created); err != nil {
		return Worker{}, err
	}
	w.Enabled = enabled != 0
	w.CreatedAt = parseTime(created)
	if err := json.Unmarshal([]byte(args), &w.Args); err != nil {
		w.Args = nil
	}
	if w.Args == nil {
		w.Args = []string{}
	}
	return w, nil
}

// ListByProject returns the workers of a project in position order.
func (r *Workers) ListByProject(ctx context.Context, projectID string) ([]Worker, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+workerColumns+` FROM project_workers WHERE project_id = ? ORDER BY position, name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select workers: %w", err)
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Get returns one worker of a project.
func (r *Workers) Get(ctx context.Context, projectID, id string) (Worker, error) {
	w, err := scanWorker(r.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM project_workers WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("select worker: %w", err)
	}
	return w, nil
}

// Add stores a worker.
func (r *Workers) Add(ctx context.Context, w *Worker) error {
	if w.ID == "" {
		w.ID = NewID()
	}
	w.CreatedAt = now()
	args, _ := json.Marshal(w.Args)
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_workers (`+workerColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ProjectID, w.Name, w.Preset, string(args), boolInt(w.Enabled), w.Position, formatTime(w.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a worker named %q already exists: %w", w.Name, ErrConflict)
		}
		return fmt.Errorf("insert worker: %w", err)
	}
	return nil
}

// Update changes name, preset, args and enabled.
func (r *Workers) Update(ctx context.Context, w Worker) error {
	args, _ := json.Marshal(w.Args)
	res, err := r.db.ExecContext(ctx, `UPDATE project_workers SET name = ?, preset = ?, args = ?, enabled = ? WHERE project_id = ? AND id = ?`,
		w.Name, w.Preset, string(args), boolInt(w.Enabled), w.ProjectID, w.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a worker named %q already exists: %w", w.Name, ErrConflict)
		}
		return fmt.Errorf("update worker: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a worker.
func (r *Workers) Delete(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM project_workers WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete worker: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

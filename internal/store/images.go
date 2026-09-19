package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ProjectImage records which local image id a project's containers were last created
// from for one image reference, and the id before that. Pinned means the containers run
// PreviousID on purpose (rollback after a broken upstream rebuild of the same tag).
type ProjectImage struct {
	ProjectID  string
	Image      string
	CurrentID  string
	PreviousID string
	Pinned     bool
	ChangedAt  time.Time
}

// ProjectImages is the repository for image history.
type ProjectImages struct{ db *sql.DB }

const projectImageColumns = `project_id, image, current_id, previous_id, pinned, changed_at`

func scanProjectImage(row interface{ Scan(...any) error }) (ProjectImage, error) {
	var pi ProjectImage
	var pinned int
	var changed string
	if err := row.Scan(&pi.ProjectID, &pi.Image, &pi.CurrentID, &pi.PreviousID, &pinned, &changed); err != nil {
		return ProjectImage{}, err
	}
	pi.Pinned = pinned != 0
	pi.ChangedAt = parseTime(changed)
	return pi, nil
}

// Get returns the record for one project and image reference, or ErrNotFound.
func (r *ProjectImages) Get(ctx context.Context, projectID, image string) (ProjectImage, error) {
	pi, err := scanProjectImage(r.db.QueryRowContext(ctx, `SELECT `+projectImageColumns+` FROM project_images WHERE project_id = ? AND image = ?`, projectID, image))
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectImage{}, ErrNotFound
	}
	if err != nil {
		return ProjectImage{}, fmt.Errorf("select project image: %w", err)
	}
	return pi, nil
}

// ListByProject returns the records of a project.
func (r *ProjectImages) ListByProject(ctx context.Context, projectID string) ([]ProjectImage, error) {
	return r.list(ctx, `SELECT `+projectImageColumns+` FROM project_images WHERE project_id = ? ORDER BY image`, projectID)
}

// List returns every record (prune protection).
func (r *ProjectImages) List(ctx context.Context) ([]ProjectImage, error) {
	return r.list(ctx, `SELECT `+projectImageColumns+` FROM project_images ORDER BY project_id, image`)
}

func (r *ProjectImages) list(ctx context.Context, query string, args ...any) ([]ProjectImage, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select project images: %w", err)
	}
	defer rows.Close()
	out := []ProjectImage{}
	for rows.Next() {
		pi, err := scanProjectImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}

// Upsert writes the record; ChangedAt is set to now when zero.
func (r *ProjectImages) Upsert(ctx context.Context, pi ProjectImage) error {
	if pi.ChangedAt.IsZero() {
		pi.ChangedAt = now()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_images (`+projectImageColumns+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, image) DO UPDATE SET current_id = excluded.current_id, previous_id = excluded.previous_id, pinned = excluded.pinned, changed_at = excluded.changed_at`,
		pi.ProjectID, pi.Image, pi.CurrentID, pi.PreviousID, boolInt(pi.Pinned), formatTime(pi.ChangedAt))
	if err != nil {
		return fmt.Errorf("upsert project image: %w", err)
	}
	return nil
}

// SetPinned flips the rollback flag of an existing record.
func (r *ProjectImages) SetPinned(ctx context.Context, projectID, image string, pinned bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE project_images SET pinned = ? WHERE project_id = ? AND image = ?`, boolInt(pinned), projectID, image)
	if err != nil {
		return fmt.Errorf("pin project image: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

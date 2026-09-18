package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Domain is an extra hostname routed to a project by the embedded proxy. The default
// hostname <slug>.<base domain> is derived and not stored.
type Domain struct {
	ID        string
	ProjectID string
	Hostname  string
	CreatedAt time.Time
}

// Domains is the repository for project hostnames.
type Domains struct{ db *sql.DB }

// Add stores a hostname for a project.
func (r *Domains) Add(ctx context.Context, projectID, hostname string) (Domain, error) {
	d := Domain{ID: NewID(), ProjectID: projectID, Hostname: strings.ToLower(hostname), CreatedAt: now()}
	_, err := r.db.ExecContext(ctx, `INSERT INTO domains (id, project_id, hostname, is_primary, created_at) VALUES (?, ?, ?, 0, ?)`,
		d.ID, d.ProjectID, d.Hostname, formatTime(d.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return Domain{}, fmt.Errorf("hostname %s is already used: %w", d.Hostname, ErrConflict)
		}
		return Domain{}, fmt.Errorf("insert domain: %w", err)
	}
	return d, nil
}

// ListByProject returns the hostnames of a project.
func (r *Domains) ListByProject(ctx context.Context, projectID string) ([]Domain, error) {
	return r.list(ctx, `SELECT id, project_id, hostname, created_at FROM domains WHERE project_id = ? ORDER BY hostname`, projectID)
}

// ListAll returns every hostname.
func (r *Domains) ListAll(ctx context.Context) ([]Domain, error) {
	return r.list(ctx, `SELECT id, project_id, hostname, created_at FROM domains ORDER BY hostname`)
}

func (r *Domains) list(ctx context.Context, query string, args ...any) ([]Domain, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select domains: %w", err)
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		var created string
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Hostname, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTime(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Delete removes a hostname of a project.
func (r *Domains) Delete(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM domains WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

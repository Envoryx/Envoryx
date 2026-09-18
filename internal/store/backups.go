package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Backup is a stored backup record; the files live under /config/backups.
type Backup struct {
	ID        string
	ProjectID string
	Filename  string // directory name relative to /config/backups/<slug>/
	SizeBytes int64
	Kind      string // full | database | files
	Metadata  json.RawMessage
	CreatedAt time.Time
}

// Backups is the repository for backup records.
type Backups struct{ db *sql.DB }

const backupColumns = `id, project_id, filename, size_bytes, kind, metadata, created_at`

func scanBackup(row interface{ Scan(...any) error }) (Backup, error) {
	var b Backup
	var meta, created string
	if err := row.Scan(&b.ID, &b.ProjectID, &b.Filename, &b.SizeBytes, &b.Kind, &meta, &created); err != nil {
		return Backup{}, err
	}
	b.Metadata = json.RawMessage(meta)
	b.CreatedAt = parseTime(created)
	return b, nil
}

// Create inserts a backup record.
func (r *Backups) Create(ctx context.Context, b *Backup) error {
	if b.ID == "" {
		b.ID = NewID()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now()
	}
	if len(b.Metadata) == 0 {
		b.Metadata = json.RawMessage("{}")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO backups (`+backupColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.Filename, b.SizeBytes, b.Kind, string(b.Metadata), formatTime(b.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert backup: %w", err)
	}
	return nil
}

// ListByProject returns the backups of a project, newest first.
func (r *Backups) ListByProject(ctx context.Context, projectID string) ([]Backup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select backups: %w", err)
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Get returns one backup of a project.
func (r *Backups) Get(ctx context.Context, projectID, id string) (Backup, error) {
	b, err := scanBackup(r.db.QueryRowContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE id = ? AND project_id = ?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Backup{}, ErrNotFound
	}
	if err != nil {
		return Backup{}, fmt.Errorf("select backup: %w", err)
	}
	return b, nil
}

// Delete removes a backup record.
func (r *Backups) Delete(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM backups WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

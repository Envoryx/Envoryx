package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Offsite upload scopes and states.
const (
	OffsiteProject  = "project"
	OffsiteInstance = "instance"

	OffsitePending = "pending"
	OffsiteRunning = "running"
	OffsiteDone    = "done"
	OffsiteFailed  = "failed"
)

// OffsiteUpload is the copy of one backup on one offsite target.
type OffsiteUpload struct {
	ID            string
	TargetID      string
	Scope         string
	ProjectID     string
	BackupID      string
	Source        string
	Status        string
	RemoteKey     string
	SizeBytes     int64
	Error         string
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OffsiteUploads is the repository for offsite copies.
type OffsiteUploads struct{ db *sql.DB }

const offsiteColumns = `id, target_id, scope, project_id, backup_id, source, status, remote_key, size_bytes, error, attempts, next_attempt_at, created_at, updated_at`

func scanOffsite(row interface{ Scan(...any) error }) (OffsiteUpload, error) {
	var u OffsiteUpload
	var next, created, updated string
	if err := row.Scan(&u.ID, &u.TargetID, &u.Scope, &u.ProjectID, &u.BackupID, &u.Source, &u.Status, &u.RemoteKey, &u.SizeBytes, &u.Error, &u.Attempts, &next, &created, &updated); err != nil {
		return OffsiteUpload{}, err
	}
	if next != "" {
		u.NextAttemptAt = parseTime(next)
	}
	u.CreatedAt, u.UpdatedAt = parseTime(created), parseTime(updated)
	return u, nil
}

func (r *OffsiteUploads) query(ctx context.Context, where string, args ...any) ([]OffsiteUpload, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+offsiteColumns+` FROM offsite_uploads `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select offsite uploads: %w", err)
	}
	defer rows.Close()
	out := []OffsiteUpload{}
	for rows.Next() {
		u, err := scanOffsite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Enqueue asks for a backup to be copied to a target. A copy that exists already is
// kept; a failed or pending one is queued again from scratch.
func (r *OffsiteUploads) Enqueue(ctx context.Context, u OffsiteUpload) (OffsiteUpload, error) {
	t := formatTime(now())
	_, err := r.db.ExecContext(ctx, `INSERT INTO offsite_uploads (id, target_id, scope, project_id, backup_id, source, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(target_id, scope, backup_id) DO UPDATE SET
			status = CASE WHEN status IN ('done', 'running') THEN status ELSE 'pending' END,
			error = CASE WHEN status IN ('done', 'running') THEN error ELSE '' END,
			attempts = CASE WHEN status IN ('done', 'running') THEN attempts ELSE 0 END,
			next_attempt_at = CASE WHEN status IN ('done', 'running') THEN next_attempt_at ELSE '' END,
			updated_at = excluded.updated_at`,
		NewID(), u.TargetID, u.Scope, u.ProjectID, u.BackupID, u.Source, OffsitePending, t, t)
	if err != nil {
		return OffsiteUpload{}, fmt.Errorf("enqueue offsite upload: %w", err)
	}
	return r.Find(ctx, u.TargetID, u.Scope, u.BackupID)
}

// Find returns the copy of a backup on a target.
func (r *OffsiteUploads) Find(ctx context.Context, targetID, scope, backupID string) (OffsiteUpload, error) {
	u, err := scanOffsite(r.db.QueryRowContext(ctx, `SELECT `+offsiteColumns+` FROM offsite_uploads WHERE target_id = ? AND scope = ? AND backup_id = ?`, targetID, scope, backupID))
	if errors.Is(err, sql.ErrNoRows) {
		return OffsiteUpload{}, ErrNotFound
	}
	return u, err
}

// Due returns the queued uploads that may run now, oldest first.
func (r *OffsiteUploads) Due(ctx context.Context, at time.Time, maxAttempts int) ([]OffsiteUpload, error) {
	return r.query(ctx, `WHERE status = 'pending' OR (status = 'failed' AND attempts < ? AND next_attempt_at != '' AND next_attempt_at <= ?) ORDER BY created_at`, maxAttempts, formatTime(at))
}

// ByBackups returns the copies of the given backups.
func (r *OffsiteUploads) ByBackups(ctx context.Context, scope string, backupIDs []string) ([]OffsiteUpload, error) {
	if len(backupIDs) == 0 {
		return []OffsiteUpload{}, nil
	}
	args := []any{scope}
	marks := ""
	for i, id := range backupIDs {
		if i > 0 {
			marks += ","
		}
		marks += "?"
		args = append(args, id)
	}
	return r.query(ctx, `WHERE scope = ? AND backup_id IN (`+marks+`) ORDER BY created_at`, args...)
}

// Recent returns the latest rows (for the settings overview), newest first.
func (r *OffsiteUploads) Recent(ctx context.Context, limit int) ([]OffsiteUpload, error) {
	return r.query(ctx, `ORDER BY updated_at DESC LIMIT ?`, limit)
}

// MarkRunning claims an upload.
func (r *OffsiteUploads) MarkRunning(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE offsite_uploads SET status = 'running', attempts = attempts + 1, updated_at = ? WHERE id = ?`, formatTime(now()), id)
	return err
}

// MarkDone records a finished copy.
func (r *OffsiteUploads) MarkDone(ctx context.Context, id, remoteKey string, size int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE offsite_uploads SET status = 'done', remote_key = ?, size_bytes = ?, error = '', next_attempt_at = '', updated_at = ? WHERE id = ?`, remoteKey, size, formatTime(now()), id)
	return err
}

// MarkFailed records a failure; next is when to try again (zero: not automatically).
func (r *OffsiteUploads) MarkFailed(ctx context.Context, id, message string, next time.Time) error {
	n := ""
	if !next.IsZero() {
		n = formatTime(next)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE offsite_uploads SET status = 'failed', error = ?, next_attempt_at = ?, updated_at = ? WHERE id = ?`, message, n, formatTime(now()), id)
	return err
}

// Record stores a copy that is known to exist (fetched from the target).
func (r *OffsiteUploads) Record(ctx context.Context, u OffsiteUpload) error {
	t := formatTime(now())
	_, err := r.db.ExecContext(ctx, `INSERT INTO offsite_uploads (id, target_id, scope, project_id, backup_id, source, status, remote_key, size_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'done', ?, ?, ?, ?)
		ON CONFLICT(target_id, scope, backup_id) DO UPDATE SET status = 'done', remote_key = excluded.remote_key, size_bytes = excluded.size_bytes, error = '', updated_at = excluded.updated_at`,
		NewID(), u.TargetID, u.Scope, u.ProjectID, u.BackupID, u.Source, u.RemoteKey, u.SizeBytes, t, t)
	return err
}

// ResetRunning puts uploads a crash interrupted back into the queue.
func (r *OffsiteUploads) ResetRunning(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE offsite_uploads SET status = 'pending', updated_at = ? WHERE status = 'running'`, formatTime(now()))
	return err
}

// DeleteByBackup forgets the copies of a deleted local backup (they stay on the target).
func (r *OffsiteUploads) DeleteByBackup(ctx context.Context, scope, backupID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM offsite_uploads WHERE scope = ? AND backup_id = ?`, scope, backupID)
	return err
}

// DeleteByTarget forgets everything about a removed target.
func (r *OffsiteUploads) DeleteByTarget(ctx context.Context, targetID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM offsite_uploads WHERE target_id = ?`, targetID)
	return err
}

// DeleteByKey forgets a copy that was removed from the target.
func (r *OffsiteUploads) DeleteByKey(ctx context.Context, targetID, remoteKey string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM offsite_uploads WHERE target_id = ? AND remote_key = ?`, targetID, remoteKey)
	return err
}

// EnqueueIfAbsent queues a copy unless the backup has a row for the target already –
// the automatic path, which must not restart a failed upload on every pass.
func (r *OffsiteUploads) EnqueueIfAbsent(ctx context.Context, u OffsiteUpload) error {
	t := formatTime(now())
	_, err := r.db.ExecContext(ctx, `INSERT INTO offsite_uploads (id, target_id, scope, project_id, backup_id, source, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(target_id, scope, backup_id) DO NOTHING`,
		NewID(), u.TargetID, u.Scope, u.ProjectID, u.BackupID, u.Source, OffsitePending, t, t)
	return err
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GuestLink lets someone without an account see a project behind the Envoryx sign-in.
// Only the hash of its token is stored.
type GuestLink struct {
	ID        string
	ProjectID string
	Label     string
	TokenHash string
	// CreatedBy is the user name of whoever made it.
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	// UsedAt is the last time a browser came in with it; nil while unused.
	UsedAt *time.Time
}

// GuestLinks is the repository for guest links.
type GuestLinks struct{ db *sql.DB }

const guestLinkColumns = `id, project_id, label, token_hash, created_by, created_at, expires_at, used_at`

// Create stores a link.
func (r *GuestLinks) Create(ctx context.Context, l GuestLink) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO guest_links (`+guestLinkColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		l.ID, l.ProjectID, l.Label, l.TokenHash, l.CreatedBy, formatTime(l.CreatedAt), formatTime(l.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert guest link: %w", err)
	}
	return nil
}

// ListByProject returns a project's links, newest first.
func (r *GuestLinks) ListByProject(ctx context.Context, projectID string) ([]GuestLink, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+guestLinkColumns+` FROM guest_links WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select guest links: %w", err)
	}
	defer rows.Close()
	var out []GuestLink
	for rows.Next() {
		l, err := scanGuestLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ByHash returns the link with this token hash.
func (r *GuestLinks) ByHash(ctx context.Context, hash string) (GuestLink, error) {
	return r.one(ctx, `SELECT `+guestLinkColumns+` FROM guest_links WHERE token_hash = ?`, hash)
}

// Get returns a link by id.
func (r *GuestLinks) Get(ctx context.Context, id string) (GuestLink, error) {
	return r.one(ctx, `SELECT `+guestLinkColumns+` FROM guest_links WHERE id = ?`, id)
}

func (r *GuestLinks) one(ctx context.Context, query string, arg string) (GuestLink, error) {
	l, err := scanGuestLink(r.db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return GuestLink{}, ErrNotFound
	}
	return l, err
}

// Touch records that the link was used.
func (r *GuestLinks) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE guest_links SET used_at = ? WHERE id = ?`, formatTime(at), id)
	return err
}

// Delete removes a link of a project.
func (r *GuestLinks) Delete(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM guest_links WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return fmt.Errorf("delete guest link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes the links that ended before at.
func (r *GuestLinks) DeleteExpired(ctx context.Context, at time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM guest_links WHERE expires_at < ?`, formatTime(at))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanGuestLink(row interface{ Scan(...any) error }) (GuestLink, error) {
	var l GuestLink
	var created, expires string
	var used sql.NullString
	if err := row.Scan(&l.ID, &l.ProjectID, &l.Label, &l.TokenHash, &l.CreatedBy, &created, &expires, &used); err != nil {
		return GuestLink{}, err
	}
	l.CreatedAt, l.ExpiresAt = parseTime(created), parseTime(expires)
	if used.Valid {
		t := parseTime(used.String)
		l.UsedAt = &t
	}
	return l, nil
}

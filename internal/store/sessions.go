package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Sessions is the repository for login sessions.
type Sessions struct{ db *sql.DB }

const sessionColumns = `id, user_id, created_at, expires_at, last_seen_at, ip, user_agent`

// Create stores a new session. ID must already be the hashed token.
func (r *Sessions) Create(ctx context.Context, s Session) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, formatTime(s.CreatedAt), formatTime(s.ExpiresAt), formatTime(s.LastSeenAt), s.IP, s.UserAgent)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Get returns the session with the given (hashed) ID.
func (r *Sessions) Get(ctx context.Context, id string) (Session, error) {
	var s Session
	var created, expires, seen string
	err := r.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id).
		Scan(&s.ID, &s.UserID, &created, &expires, &seen, &s.IP, &s.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("select session: %w", err)
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = parseTime(created), parseTime(expires), parseTime(seen)
	return s, nil
}

// Touch updates the last-seen timestamp and expiry of a session.
func (r *Sessions) Touch(ctx context.Context, id string, lastSeen, expires time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		formatTime(lastSeen), formatTime(expires), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes one session.
func (r *Sessions) Delete(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteByUser removes all sessions of a user (e.g. after a password change).
func (r *Sessions) DeleteByUser(ctx context.Context, userID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// DeleteExpired purges sessions whose expiry is in the past.
func (r *Sessions) DeleteExpired(ctx context.Context, ref time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, formatTime(ref))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

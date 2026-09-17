package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Settings is a simple key/value repository.
type Settings struct{ db *sql.DB }

// Get returns the value for key or ErrNotFound.
func (r *Settings) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select setting: %w", err)
	}
	return v, nil
}

// Set upserts a value.
func (r *Settings) Set(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTime(now()))
	if err != nil {
		return fmt.Errorf("upsert setting: %w", err)
	}
	return nil
}

// All returns every setting.
func (r *Settings) All(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("select settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Audit is the append-only audit log repository.
type Audit struct{ db *sql.DB }

// Append writes one audit entry. Details must not contain secrets.
func (r *Audit) Append(ctx context.Context, e AuditEntry) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now()
	}
	if len(e.Details) == 0 {
		e.Details = json.RawMessage("{}")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO audit_log (id, created_at, user_id, username, action, target_type, target_id, details, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, formatTime(e.CreatedAt), e.UserID, e.Username, e.Action, e.TargetType, e.TargetID, string(e.Details), e.IP)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// Recent returns the newest entries, newest first.
func (r *Audit) Recent(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, created_at, user_id, username, action, target_type, target_id, details, ip
		 FROM audit_log ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("select audit log: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var created, details string
		if err := rows.Scan(&e.ID, &created, &e.UserID, &e.Username, &e.Action, &e.TargetType, &e.TargetID, &details, &e.IP); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		e.Details = json.RawMessage(details)
		out = append(out, e)
	}
	return out, rows.Err()
}

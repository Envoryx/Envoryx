package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// APIToken is a long-lived bearer credential for MCP and automation clients. Only the
// hash of the secret is stored.
type APIToken struct {
	ID        string
	UserID    string
	Name      string
	TokenHash string
	Prefix    string
	// Scope is the access level: "read", "operate" or "admin" (see package auth).
	Scope string
	// ProjectIDs confines the token to these projects; empty means all projects.
	ProjectIDs []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// APITokens is the repository for API tokens.
type APITokens struct{ db *sql.DB }

const tokenColumns = `id, user_id, name, token_hash, prefix, scope, project_ids, created_at, last_used_at`

// Create stores a token.
func (r *APITokens) Create(ctx context.Context, t APIToken) error {
	projects, err := json.Marshal(nonNil(t.ProjectIDs))
	if err != nil {
		return fmt.Errorf("encode project ids: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		t.ID, t.UserID, t.Name, t.TokenHash, t.Prefix, t.Scope, string(projects), formatTime(t.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("token already exists: %w", ErrConflict)
		}
		return fmt.Errorf("insert api token: %w", err)
	}
	return nil
}

// GetByHash looks a token up by the hash of its secret.
func (r *APITokens) GetByHash(ctx context.Context, hash string) (APIToken, error) {
	return r.one(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE token_hash = ?`, hash)
}

// Get returns a token by id.
func (r *APITokens) Get(ctx context.Context, id string) (APIToken, error) {
	return r.one(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id = ?`, id)
}

func (r *APITokens) one(ctx context.Context, query string, arg any) (APIToken, error) {
	t, err := scanToken(r.db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("select api token: %w", err)
	}
	return t, nil
}

func scanToken(row interface{ Scan(...any) error }) (APIToken, error) {
	var t APIToken
	var created, projects string
	var used sql.NullString
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.Scope, &projects, &created, &used); err != nil {
		return APIToken{}, err
	}
	if err := json.Unmarshal([]byte(projects), &t.ProjectIDs); err != nil {
		return APIToken{}, fmt.Errorf("decode project ids: %w", err)
	}
	t.ProjectIDs = nonNil(t.ProjectIDs)
	t.CreatedAt = parseTime(created)
	if used.Valid {
		u := parseTime(used.String)
		t.LastUsedAt = &u
	}
	return t, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// List returns all tokens, newest first.
func (r *APITokens) List(ctx context.Context) ([]APIToken, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("select api tokens: %w", err)
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Touch records the last use of a token.
func (r *APITokens) Touch(ctx context.Context, id string, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}

// Delete revokes a token.
func (r *APITokens) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

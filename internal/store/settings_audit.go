package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
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

// AuditQuery narrows the audit log. Zero values do not filter.
type AuditQuery struct {
	// Text matches action, user, target, IP and details (case-insensitive substring).
	Text string
	// User is an account name; entries of its API tokens ("admin (token: ci)") count too.
	User string
	// Actions are prefixes ("project.", "auth.login"); an entry matches any of them.
	Actions []string
	// TargetID is a project (or other target) id.
	TargetID string
	Since    time.Time
	Until    time.Time
	// After is the cursor from the previous page (Query's next).
	After string
	// Limit caps a page (1–1000, default 100); Each ignores it.
	Limit int
}

func (q AuditQuery) where() (string, []any) {
	var conds []string
	var args []any
	like := func(s string) string {
		return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(s)) + "%"
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		conds = append(conds, `(lower(action) LIKE ? ESCAPE '\' OR lower(username) LIKE ? ESCAPE '\' OR lower(target_id) LIKE ? ESCAPE '\' OR lower(ip) LIKE ? ESCAPE '\' OR lower(details) LIKE ? ESCAPE '\')`)
		l := like(t)
		args = append(args, l, l, l, l, l)
	}
	if q.User != "" {
		conds = append(conds, `(username = ? OR substr(username, 1, ?) = ?)`)
		prefix := q.User + " (token: "
		args = append(args, q.User, len(prefix), prefix)
	}
	if len(q.Actions) > 0 {
		var ors []string
		for _, a := range q.Actions {
			ors = append(ors, `substr(action, 1, ?) = ?`)
			args = append(args, len(a), a)
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	if q.TargetID != "" {
		conds = append(conds, `target_id = ?`)
		args = append(args, q.TargetID)
	}
	if !q.Since.IsZero() {
		conds = append(conds, `created_at >= ?`)
		args = append(args, formatTime(q.Since))
	}
	if !q.Until.IsZero() {
		conds = append(conds, `created_at < ?`)
		args = append(args, formatTime(q.Until))
	}
	if created, id, ok := strings.Cut(q.After, "|"); ok {
		// Keyset paging in the order the page was sorted: no entry twice, none skipped,
		// whatever is written meanwhile.
		conds = append(conds, `(created_at < ? OR (created_at = ? AND id < ?))`)
		args = append(args, created, created, id)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Query returns a page of matching entries, newest first, and the cursor of the next
// page ("" = this was the last).
func (r *Audit) Query(ctx context.Context, q AuditQuery) ([]AuditEntry, string, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 100
	}
	where, args := q.where()
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, created_at, user_id, username, action, target_type, target_id, details, ip
		 FROM audit_log`+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("select audit log: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	var raw []string
	for rows.Next() {
		e, created, err := scanAudit(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
		raw = append(raw, created)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > q.Limit {
		out, raw = out[:q.Limit], raw[:q.Limit]
		next = raw[len(raw)-1] + "|" + out[len(out)-1].ID
	}
	return out, next, nil
}

// Each streams every matching entry, newest first, to fn (the export).
func (r *Audit) Each(ctx context.Context, q AuditQuery, fn func(AuditEntry) error) error {
	where, args := q.where()
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, created_at, user_id, username, action, target_type, target_id, details, ip
		 FROM audit_log`+where+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return fmt.Errorf("select audit log: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e, _, err := scanAudit(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanAudit(rows *sql.Rows) (AuditEntry, string, error) {
	var e AuditEntry
	var created, details string
	if err := rows.Scan(&e.ID, &created, &e.UserID, &e.Username, &e.Action, &e.TargetType, &e.TargetID, &details, &e.IP); err != nil {
		return AuditEntry{}, "", err
	}
	e.CreatedAt = parseTime(created)
	e.Details = json.RawMessage(details)
	return e, created, nil
}

// Users lists the account names that appear in the log (tokens under their account),
// for the user filter.
func (r *Audit) Users(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT username FROM audit_log WHERE username != ''`)
	if err != nil {
		return nil, fmt.Errorf("select audit users: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		if base, _, ok := strings.Cut(u, " (token: "); ok {
			u = base
		}
		seen[u] = true
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Prune deletes the entries older than before and returns how many went.
func (r *Audit) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune audit log: %w", err)
	}
	return res.RowsAffected()
}

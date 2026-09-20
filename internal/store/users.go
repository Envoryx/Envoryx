package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Users is the repository for login accounts.
type Users struct{ db *sql.DB }

const userColumns = `id, username, password_hash, role, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, updated string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created, &updated); err != nil {
		return User{}, err
	}
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

// Count returns the number of users.
func (r *Users) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// Create inserts a new user. The caller supplies an already hashed password.
func (r *Users) Create(ctx context.Context, username, passwordHash, role string) (User, error) {
	u := User{
		ID:           NewID(),
		Username:     username,
		PasswordHash: passwordHash,
		Role:         role,
		CreatedAt:    now(),
	}
	u.UpdatedAt = u.CreatedAt
	_, err := r.db.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Role, formatTime(u.CreatedAt), formatTime(u.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, fmt.Errorf("username %q: %w", username, ErrConflict)
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// ByUsername looks a user up by (case-insensitive) username.
func (r *Users) ByUsername(ctx context.Context, username string) (User, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user: %w", err)
	}
	return u, nil
}

// ByID looks a user up by ID.
func (r *Users) ByID(ctx context.Context, id string) (User, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user: %w", err)
	}
	return u, nil
}

// UpdatePassword replaces the password hash of a user.
func (r *Users) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, formatTime(now()), id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every account, oldest first.
func (r *Users) List(ctx context.Context) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at, username`)
	if err != nil {
		return nil, fmt.Errorf("select users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteAll removes every account; sessions and API tokens go with them (ON DELETE
// CASCADE). Afterwards the setup page is shown again. Used by the rescue CLI only.
func (r *Users) DeleteAll(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM users`)
	if err != nil {
		return 0, fmt.Errorf("delete users: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

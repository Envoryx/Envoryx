package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Users is the repository for login accounts.
type Users struct{ db *sql.DB }

const userColumns = `id, username, password_hash, role, invite_hash, invite_expires_at, oidc_subject, disabled, ssh_keys, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, updated, inviteExpires string
	var disabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.InviteHash, &inviteExpires, &u.OIDCSubject, &disabled, &u.SSHKeys, &created, &updated); err != nil {
		return User{}, err
	}
	if inviteExpires != "" {
		u.InviteExpiresAt = parseTime(inviteExpires)
	}
	u.Disabled = disabled == 1
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
	return r.Insert(ctx, User{Username: username, PasswordHash: passwordHash, Role: role})
}

// Insert adds a user with every field the caller set (an invitation, an OpenID Connect
// subject); ID and timestamps are filled in.
func (r *Users) Insert(ctx context.Context, u User) (User, error) {
	u.ID = NewID()
	u.CreatedAt = now()
	u.UpdatedAt = u.CreatedAt
	expires := ""
	if !u.InviteExpiresAt.IsZero() {
		expires = formatTime(u.InviteExpiresAt)
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Role, u.InviteHash, expires, u.OIDCSubject, boolInt(u.Disabled), u.SSHKeys, formatTime(u.CreatedAt), formatTime(u.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, fmt.Errorf("username %q: %w", u.Username, ErrConflict)
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// ByInvite looks a user up by the hash of an invitation token.
func (r *Users) ByInvite(ctx context.Context, hash string) (User, error) {
	if hash == "" {
		return User{}, ErrNotFound
	}
	u, err := scanUser(r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE invite_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user: %w", err)
	}
	return u, nil
}

// ByOIDCSubject looks a user up by the OpenID Connect subject it is linked to.
func (r *Users) ByOIDCSubject(ctx context.Context, subject string) (User, error) {
	if subject == "" {
		return User{}, ErrNotFound
	}
	u, err := scanUser(r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE oidc_subject = ?`, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user: %w", err)
	}
	return u, nil
}

// update runs one UPDATE of a user and reports ErrNotFound when no row matched.
func (r *Users) update(ctx context.Context, what, query string, args ...any) error {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%s: %w", what, ErrConflict)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRole changes a user's global role.
func (r *Users) SetRole(ctx context.Context, id, role string) error {
	return r.update(ctx, "set role", `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, formatTime(now()), id)
}

// SetDisabled disables or enables a user.
func (r *Users) SetDisabled(ctx context.Context, id string, disabled bool) error {
	return r.update(ctx, "set disabled", `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, boolInt(disabled), formatTime(now()), id)
}

// SetInvite stores a new invitation (hash and expiry) for a user; an empty hash clears it.
func (r *Users) SetInvite(ctx context.Context, id, hash string, expires time.Time) error {
	exp := ""
	if hash != "" {
		exp = formatTime(expires)
	}
	return r.update(ctx, "set invite", `UPDATE users SET invite_hash = ?, invite_expires_at = ?, updated_at = ? WHERE id = ?`, hash, exp, formatTime(now()), id)
}

// AcceptInvite sets the password of an invited user and ends the invitation, provided
// the invitation is still the one with this hash.
func (r *Users) AcceptInvite(ctx context.Context, id, hash, passwordHash string) error {
	return r.update(ctx, "accept invite", `UPDATE users SET password_hash = ?, invite_hash = '', invite_expires_at = '', updated_at = ? WHERE id = ? AND invite_hash = ?`, passwordHash, formatTime(now()), id, hash)
}

// SetOIDCSubject links a user to an OpenID Connect account.
func (r *Users) SetOIDCSubject(ctx context.Context, id, subject string) error {
	return r.update(ctx, "link oidc account", `UPDATE users SET oidc_subject = ?, updated_at = ? WHERE id = ?`, subject, formatTime(now()), id)
}

// SetSSHKeys stores a user's public keys for the SSH server.
func (r *Users) SetSSHKeys(ctx context.Context, id, keys string) error {
	return r.update(ctx, "set ssh keys", `UPDATE users SET ssh_keys = ?, updated_at = ? WHERE id = ?`, keys, formatTime(now()), id)
}

// Delete removes a user with its sessions, API tokens and project roles.
func (r *Users) Delete(ctx context.Context, id string) error {
	return r.update(ctx, "delete user", `DELETE FROM users WHERE id = ?`, id)
}

// CountActiveAdmins counts the admins who can still sign in.
func (r *Users) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
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

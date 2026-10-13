package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/envoryx/envoryx/internal/secrets"
)

// TwoFactor is the repository for the users' second factors: the TOTP secret, passkeys
// and recovery codes (see auth/twofactor.go).
type TwoFactor struct{ db *sql.DB }

// Passkey is a WebAuthn credential of a user.
type Passkey struct {
	// ID is the credential id, base64url.
	ID     string
	UserID string
	Name   string
	// RPID is the host name the passkey was registered for; it only works there.
	RPID string
	// Credential is the WebAuthn library's JSON of the credential (public key, counter).
	Credential string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// Factors is what a user has set up.
type Factors struct {
	TOTP          bool
	Passkeys      int
	RecoveryCodes int
}

// Any reports whether the user has a second factor.
func (f Factors) Any() bool { return f.TOTP || f.Passkeys > 0 }

// Factors returns what the user has set up.
func (r *TwoFactor) Factors(ctx context.Context, userID string) (Factors, error) {
	var f Factors
	var totp int
	err := r.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM user_totp WHERE user_id = ?),
		(SELECT COUNT(*) FROM user_passkeys WHERE user_id = ?),
		(SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ?)`, userID, userID, userID).Scan(&totp, &f.Passkeys, &f.RecoveryCodes)
	if err != nil {
		return Factors{}, fmt.Errorf("select factors: %w", err)
	}
	f.TOTP = totp > 0
	return f, nil
}

// WithFactors returns the ids of the users who have a second factor.
func (r *TwoFactor) WithFactors(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT user_id FROM user_totp UNION SELECT user_id FROM user_passkeys`)
	if err != nil {
		return nil, fmt.Errorf("select users with factors: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// TOTP returns the user's TOTP secret (base32) and the last time step a code was taken
// for; ErrNotFound when the user has none.
func (r *TwoFactor) TOTP(ctx context.Context, userID string) (secret string, lastStep int64, err error) {
	var sealed string
	err = r.db.QueryRowContext(ctx, `SELECT secret, last_step FROM user_totp WHERE user_id = ?`, userID).Scan(&sealed, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("select totp: %w", err)
	}
	secret, err = secrets.Open(sealed)
	if err != nil {
		return "", 0, fmt.Errorf("totp secret: %w", err)
	}
	return secret, lastStep, nil
}

// SetTOTP stores the user's TOTP secret (replacing one there is), with step as the time
// step of the code that confirmed it.
func (r *TwoFactor) SetTOTP(ctx context.Context, userID, secret string, step int64) error {
	sealed, err := secrets.Seal(secret)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO user_totp (user_id, secret, last_step, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET secret = excluded.secret, last_step = excluded.last_step, created_at = excluded.created_at`,
		userID, sealed, step, formatTime(now()))
	if err != nil {
		return fmt.Errorf("store totp: %w", err)
	}
	return nil
}

// UseTOTPStep records that a code of step was taken. It reports false when a code of
// that step or a later one was taken already: a code works once.
func (r *TwoFactor) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, fmt.Errorf("use totp step: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTOTP removes the user's TOTP secret.
func (r *TwoFactor) DeleteTOTP(ctx context.Context, userID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM user_totp WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete totp: %w", err)
	}
	return nil
}

const passkeyColumns = `id, user_id, name, rp_id, credential, created_at, last_used_at`

func scanPasskey(row interface{ Scan(...any) error }) (Passkey, error) {
	var p Passkey
	var created, used string
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.RPID, &p.Credential, &created, &used); err != nil {
		return Passkey{}, err
	}
	p.CreatedAt = parseTime(created)
	if used != "" {
		p.LastUsedAt = parseTime(used)
	}
	return p, nil
}

// Passkeys returns the user's passkeys, oldest first.
func (r *TwoFactor) Passkeys(ctx context.Context, userID string) ([]Passkey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+passkeyColumns+` FROM user_passkeys WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("select passkeys: %w", err)
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Passkey returns one passkey by its credential id.
func (r *TwoFactor) Passkey(ctx context.Context, id string) (Passkey, error) {
	p, err := scanPasskey(r.db.QueryRowContext(ctx, `SELECT `+passkeyColumns+` FROM user_passkeys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	if err != nil {
		return Passkey{}, fmt.Errorf("select passkey: %w", err)
	}
	return p, nil
}

// AddPasskey stores a new passkey.
func (r *TwoFactor) AddPasskey(ctx context.Context, p Passkey) (Passkey, error) {
	p.CreatedAt = now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO user_passkeys (`+passkeyColumns+`) VALUES (?, ?, ?, ?, ?, ?, '')`,
		p.ID, p.UserID, p.Name, p.RPID, p.Credential, formatTime(p.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return Passkey{}, fmt.Errorf("passkey: %w", ErrConflict)
		}
		return Passkey{}, fmt.Errorf("insert passkey: %w", err)
	}
	return p, nil
}

// UsePasskey stores the credential after a sign-in (its counter moves) and when.
func (r *TwoFactor) UsePasskey(ctx context.Context, id, credential string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_passkeys SET credential = ?, last_used_at = ? WHERE id = ?`, credential, formatTime(now()), id)
	if err != nil {
		return fmt.Errorf("update passkey: %w", err)
	}
	return nil
}

// RenamePasskey renames one of the user's passkeys.
func (r *TwoFactor) RenamePasskey(ctx context.Context, userID, id, name string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE user_passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes one of the user's passkeys.
func (r *TwoFactor) DeletePasskey(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM user_passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRecoveryCodes replaces the user's recovery codes with the given hashes.
func (r *TwoFactor) SetRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_recovery_codes (user_id, hash) VALUES (?, ?)`, userID, h); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}
	return tx.Commit()
}

// UseRecoveryCode removes the code with the hash and reports whether the user had it.
func (r *TwoFactor) UseRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = ? AND hash = ?`, userID, hash)
	if err != nil {
		return false, fmt.Errorf("use recovery code: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Reset removes every second factor of the user and the recovery codes.
func (r *TwoFactor) Reset(ctx context.Context, userID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM user_totp WHERE user_id = ?`,
		`DELETE FROM user_passkeys WHERE user_id = ?`,
		`DELETE FROM user_recovery_codes WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return fmt.Errorf("reset second factors: %w", err)
		}
	}
	return tx.Commit()
}

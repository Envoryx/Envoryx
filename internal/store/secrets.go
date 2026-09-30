package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/envoryx/envoryx/internal/secrets"
)

// secretCheckValue is what the SettingSecretCheck setting holds, sealed.
const secretCheckValue = "envoryx"

// CheckSecretKey proves the process's key ring opens the database's secrets: the sealed
// check value must open. A database without one (new, or from before encryption) gets
// it. The error names the key the database needs.
func (s *Store) CheckSecretKey(ctx context.Context) error {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SettingSecretCheck).Scan(&raw)
	if err != nil {
		return s.Settings.Set(ctx, SettingSecretCheck, secretCheckValue)
	}
	v, err := secrets.Open(raw)
	if err != nil {
		if errors.Is(err, secrets.ErrUnknownKey) {
			return fmt.Errorf("the database's secrets were encrypted with key %s, which Envoryx was not given: set %s to that key for one start (Envoryx re-encrypts everything with its current key), set it as %s, or put its key file back as /config/%s (Settings, Secret key shows the key of a running instance)",
				secrets.KeyIDOf(raw), secrets.EnvOldKey, secrets.EnvKey, secrets.KeyFile)
		}
		return err
	}
	if v != secretCheckValue {
		return errors.New("the secret check value does not match")
	}
	return nil
}

// ResealSecrets seals every secret in the database with the current key: plain values
// from before encryption and values sealed with an older key. It returns how many it
// changed.
func (s *Store) ResealSecrets(ctx context.Context) (int, error) {
	r := secrets.Default()
	if r == nil {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	changed := 0
	reseal := func(query, update string, args ...any) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		type row struct{ id, value string }
		var todo []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.value); err != nil {
				_ = rows.Close()
				return err
			}
			if r.NeedsReseal(x.value) {
				todo = append(todo, x)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range todo {
			v, _, err := r.Reseal(x.value)
			if err != nil {
				return fmt.Errorf("%s: %w", x.id, err)
			}
			if _, err := tx.ExecContext(ctx, update, v, x.id); err != nil {
				return err
			}
			changed++
		}
		return nil
	}
	if err := reseal(`SELECT id, git_token FROM projects`, `UPDATE projects SET git_token = ? WHERE id = ?`); err != nil {
		return 0, fmt.Errorf("reseal git tokens: %w", err)
	}
	if err := reseal(`SELECT id, value FROM project_environment_variables WHERE is_secret = 1`, `UPDATE project_environment_variables SET value = ? WHERE id = ?`); err != nil {
		return 0, fmt.Errorf("reseal variables: %w", err)
	}
	if err := reseal(`SELECT id, config FROM project_services`, `UPDATE project_services SET config = ? WHERE id = ?`); err != nil {
		return 0, fmt.Errorf("reseal service configs: %w", err)
	}
	if err := reseal(`SELECT id, config FROM project_kept_services`, `UPDATE project_kept_services SET config = ? WHERE id = ?`); err != nil {
		return 0, fmt.Errorf("reseal kept service configs: %w", err)
	}
	for key := range SecretSettings {
		if err := reseal(`SELECT key, value FROM settings WHERE key = ?`, `UPDATE settings SET value = ? WHERE key = ?`, key); err != nil {
			return 0, fmt.Errorf("reseal setting %s: %w", key, err)
		}
	}
	return changed, tx.Commit()
}

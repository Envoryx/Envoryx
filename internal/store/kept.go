package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/envoryx/envoryx/internal/secrets"
)

// KeptService is a service removed without its data: the volume stays, and so do the
// settings the data was initialised with.
type KeptService struct {
	ProjectID string
	Kind      ServiceKind
	Version   string
	Config    json.RawMessage
	KeptAt    time.Time
}

// KeepService records a removed service's settings, replacing an earlier record of the
// same kind.
func (r *Projects) KeepService(ctx context.Context, k KeptService) error {
	if len(k.Config) == 0 {
		k.Config = json.RawMessage("{}")
	}
	cfg, err := secrets.Seal(string(k.Config))
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO project_kept_services (id, project_id, kind, version, config, kept_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, kind) DO UPDATE SET version = excluded.version, config = excluded.config, kept_at = excluded.kept_at`,
		NewID(), k.ProjectID, string(k.Kind), k.Version, cfg, formatTime(now()))
	if err != nil {
		return fmt.Errorf("keep service: %w", err)
	}
	return nil
}

// KeptServices lists the services of a project whose data was kept, by kind.
func (r *Projects) KeptServices(ctx context.Context, projectID string) ([]KeptService, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, kind, version, config, kept_at FROM project_kept_services WHERE project_id = ? ORDER BY kind`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select kept services: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeptService
	for rows.Next() {
		k, err := scanKept(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// KeptService returns the kept record of one service, or ErrNotFound.
func (r *Projects) KeptService(ctx context.Context, projectID string, kind ServiceKind) (KeptService, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT project_id, kind, version, config, kept_at FROM project_kept_services WHERE project_id = ? AND kind = ?`, projectID, string(kind))
	k, err := scanKept(row)
	if errors.Is(err, sql.ErrNoRows) {
		return KeptService{}, ErrNotFound
	}
	return k, err
}

// ForgetKeptService drops the kept record of one service; a missing one is no error.
func (r *Projects) ForgetKeptService(ctx context.Context, projectID string, kind ServiceKind) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM project_kept_services WHERE project_id = ? AND kind = ?`, projectID, string(kind)); err != nil {
		return fmt.Errorf("forget kept service: %w", err)
	}
	return nil
}

func scanKept(row interface{ Scan(...any) error }) (KeptService, error) {
	var k KeptService
	var kind, cfg, at string
	if err := row.Scan(&k.ProjectID, &kind, &k.Version, &cfg, &at); err != nil {
		return KeptService{}, err
	}
	plain, err := secrets.Open(cfg)
	if err != nil {
		return KeptService{}, fmt.Errorf("kept service %s config: %w", kind, err)
	}
	k.Kind, k.Config, k.KeptAt = ServiceKind(kind), json.RawMessage(plain), parseTime(at)
	return k, nil
}

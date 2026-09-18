package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Projects is the repository for projects, their services and environment variables.
type Projects struct{ db *sql.DB }

const projectColumns = `id, name, slug, path, docroot, desired_state, http_port, lifecycle, last_error, git_url, git_branch, git_username, git_token, backup_schedule, backup_hour, backup_weekday, backup_keep, backup_include_deps, backup_last_run, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var port sql.NullInt64
	var created, updated, desired, lifecycle, lastRun string
	var includeDeps int
	if err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.Path, &p.Docroot, &desired, &port, &lifecycle, &p.LastError, &p.Git.URL, &p.Git.Branch, &p.Git.Username, &p.Git.Token,
		&p.Backup.Schedule, &p.Backup.Hour, &p.Backup.Weekday, &p.Backup.Keep, &includeDeps, &lastRun, &created, &updated); err != nil {
		return Project{}, err
	}
	p.Backup.IncludeDependencies = includeDeps == 1
	if lastRun != "" {
		p.Backup.LastRun = parseTime(lastRun)
	}
	p.DesiredState = DesiredState(desired)
	p.Lifecycle = Lifecycle(lifecycle)
	p.HTTPPort = int(port.Int64)
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return p, nil
}

// Create inserts the project with all services and env vars in one transaction.
func (r *Projects) Create(ctx context.Context, p *Project) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	ts := now()
	p.CreatedAt, p.UpdatedAt = ts, ts
	if p.DesiredState == "" {
		p.DesiredState = DesiredStopped
	}
	if p.Lifecycle == "" {
		p.Lifecycle = LifecycleCreating
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var port any
	if p.HTTPPort > 0 {
		port = p.HTTPPort
	}
	if p.Backup.Keep == 0 {
		p.Backup.Keep = 7
	}
	if p.Backup.Hour == 0 && p.Backup.Schedule == "" {
		p.Backup.Hour = 3
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Slug, p.Path, p.Docroot, string(p.DesiredState), port, string(p.Lifecycle), p.LastError,
		p.Git.URL, p.Git.Branch, p.Git.Username, p.Git.Token,
		p.Backup.Schedule, p.Backup.Hour, p.Backup.Weekday, p.Backup.Keep, boolInt(p.Backup.IncludeDependencies), "",
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("project name, slug, path or port already in use: %w", ErrConflict)
		}
		return fmt.Errorf("insert project: %w", err)
	}
	for i := range p.Services {
		s := &p.Services[i]
		if s.ID == "" {
			s.ID = NewID()
		}
		s.ProjectID = p.ID
		if len(s.Config) == 0 {
			s.Config = json.RawMessage("{}")
		}
		if err := insertService(ctx, tx, *s); err != nil {
			return err
		}
	}
	for i := range p.Env {
		e := &p.Env[i]
		if e.ID == "" {
			e.ID = NewID()
		}
		e.ProjectID = p.ID
		e.CreatedAt = ts
		if err := insertEnv(ctx, tx, *e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertService(ctx context.Context, q querier, s ProjectService) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO project_services (id, project_id, kind, variant, version, image, enabled, config, position)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, string(s.Kind), s.Variant, s.Version, s.Image, boolInt(s.Enabled), string(s.Config), s.Position)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("service %s already exists: %w", s.Kind, ErrConflict)
		}
		return fmt.Errorf("insert service: %w", err)
	}
	return nil
}

func insertEnv(ctx context.Context, q querier, e EnvVar) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO project_environment_variables (id, project_id, key, value, is_secret, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.Key, e.Value, boolInt(e.IsSecret), formatTime(e.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("environment variable %s already exists: %w", e.Key, ErrConflict)
		}
		return fmt.Errorf("insert env var: %w", err)
	}
	return nil
}

// Get loads a project including services and env vars.
func (r *Projects) Get(ctx context.Context, id string) (Project, error) {
	p, err := scanProject(r.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("select project: %w", err)
	}
	if err := r.loadChildren(ctx, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// List returns all projects with their services, ordered by name.
func (r *Projects) List(ctx context.Context) ([]Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("select projects: %w", err)
	}
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := r.loadChildren(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Projects) loadChildren(ctx context.Context, p *Project) error {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, kind, variant, version, image, enabled, config, position
		 FROM project_services WHERE project_id = ? ORDER BY position, kind`, p.ID)
	if err != nil {
		return fmt.Errorf("select services: %w", err)
	}
	p.Services = nil
	for rows.Next() {
		var s ProjectService
		var kind, cfg string
		var enabled int
		if err := rows.Scan(&s.ID, &s.ProjectID, &kind, &s.Variant, &s.Version, &s.Image, &enabled, &cfg, &s.Position); err != nil {
			_ = rows.Close()
			return err
		}
		s.Kind = ServiceKind(kind)
		s.Enabled = enabled != 0
		s.Config = json.RawMessage(cfg)
		p.Services = append(p.Services, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = r.db.QueryContext(ctx,
		`SELECT id, project_id, key, value, is_secret, created_at
		 FROM project_environment_variables WHERE project_id = ? ORDER BY key`, p.ID)
	if err != nil {
		return fmt.Errorf("select env vars: %w", err)
	}
	defer rows.Close()
	p.Env = nil
	for rows.Next() {
		var e EnvVar
		var secret int
		var created string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Key, &e.Value, &secret, &created); err != nil {
			return err
		}
		e.IsSecret = secret != 0
		e.CreatedAt = parseTime(created)
		p.Env = append(p.Env, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	workers, err := (&Workers{db: r.db}).ListByProject(ctx, p.ID)
	if err != nil {
		return err
	}
	p.Workers = workers
	return nil
}

// UpdateState sets desired state, lifecycle and last error.
func (r *Projects) UpdateState(ctx context.Context, id string, desired DesiredState, lifecycle Lifecycle, lastError string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE projects SET desired_state = ?, lifecycle = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		string(desired), string(lifecycle), lastError, formatTime(now()), id)
	if err != nil {
		return fmt.Errorf("update project state: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSettings changes user-editable project fields (name, docroot).
func (r *Projects) UpdateSettings(ctx context.Context, id, name, docroot string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE projects SET name = ?, docroot = ?, updated_at = ? WHERE id = ?`,
		name, docroot, formatTime(now()), id)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("project name already in use: %w", ErrConflict)
		}
		return fmt.Errorf("update project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateBackupSchedule stores the schedule (LastRun untouched).
func (r *Projects) UpdateBackupSchedule(ctx context.Context, id string, b BackupSchedule) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE projects SET backup_schedule = ?, backup_hour = ?, backup_weekday = ?, backup_keep = ?, backup_include_deps = ?, updated_at = ? WHERE id = ?`,
		b.Schedule, b.Hour, b.Weekday, b.Keep, boolInt(b.IncludeDependencies), formatTime(now()), id)
	if err != nil {
		return fmt.Errorf("update backup schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBackupLastRun records when the schedule last ran.
func (r *Projects) SetBackupLastRun(ctx context.Context, id string, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE projects SET backup_last_run = ? WHERE id = ?`, formatTime(at), id); err != nil {
		return fmt.Errorf("set backup last run: %w", err)
	}
	return nil
}

// UpdateGit replaces the repository binding of a project.
func (r *Projects) UpdateGit(ctx context.Context, id string, g GitConfig) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE projects SET git_url = ?, git_branch = ?, git_username = ?, git_token = ?, updated_at = ? WHERE id = ?`,
		g.URL, g.Branch, g.Username, g.Token, formatTime(now()), id)
	if err != nil {
		return fmt.Errorf("update git config: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServiceConfig replaces version, image and config of one service.
func (r *Projects) UpdateServiceConfig(ctx context.Context, projectID string, kind ServiceKind, version, image string, cfg json.RawMessage) error {
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE project_services SET version = ?, image = ?, config = ? WHERE project_id = ? AND kind = ?`,
		version, image, string(cfg), projectID, string(kind))
	if err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddService adds a service to an existing project.
func (r *Projects) AddService(ctx context.Context, s ProjectService) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	if len(s.Config) == 0 {
		s.Config = json.RawMessage("{}")
	}
	if err := insertService(ctx, r.db, s); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, formatTime(now()), s.ProjectID)
	return err
}

// DeleteService removes a service of a project.
func (r *Projects) DeleteService(ctx context.Context, projectID string, kind ServiceKind) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM project_services WHERE project_id = ? AND kind = ?`, projectID, string(kind))
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = r.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, formatTime(now()), projectID)
	return err
}

// ReplaceEnv replaces all env vars of a project atomically.
func (r *Projects) ReplaceEnv(ctx context.Context, projectID string, env []EnvVar) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_environment_variables WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete env vars: %w", err)
	}
	ts := now()
	for _, e := range env {
		e.ID = NewID()
		e.ProjectID = projectID
		e.CreatedAt = ts
		if err := insertEnv(ctx, tx, e); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, formatTime(ts), projectID); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes the project and (via cascade) its children.
func (r *Projects) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UsedPorts returns every allocated HTTP port.
func (r *Projects) UsedPorts(ctx context.Context) ([]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT http_port FROM projects WHERE http_port IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("select ports: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Count returns the number of projects.
func (r *Projects) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count projects: %w", err)
	}
	return n, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

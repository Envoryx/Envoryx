package project

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ErrNoDatabase is returned when a project has no database service.
var ErrNoDatabase = fmt.Errorf("%w: project has no database service", store.ErrNotFound)

func databaseConfig(p store.Project) (*store.ProjectService, runtime.DatabaseConfig, error) {
	svc := p.Service(store.ServiceDatabase)
	if svc == nil || !svc.Enabled {
		return nil, runtime.DatabaseConfig{}, ErrNoDatabase
	}
	var cfg runtime.DatabaseConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return nil, runtime.DatabaseConfig{}, fmt.Errorf("database config: %w", err)
	}
	return svc, cfg, nil
}

func dialectOf(svc *store.ProjectService) (runtime.Dialect, error) {
	d, ok := runtime.DialectFor(svc.Variant)
	if !ok {
		return runtime.Dialect{}, fmt.Errorf("database variant %q is not supported", svc.Variant)
	}
	return d, nil
}

func (m *Manager) saveDatabaseConfig(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return m.store.Projects.UpdateServiceConfig(ctx, p.ID, store.ServiceDatabase, svc.Version, svc.Image, raw)
}

// DatabaseInfo returns the database service description without secrets.
func (m *Manager) DatabaseInfo(ctx context.Context, id string) (DatabaseInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return DatabaseInfo{}, err
	}
	svc, cfg, err := databaseConfig(view.Project)
	if err != nil {
		return DatabaseInfo{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return DatabaseInfo{}, err
	}
	info := DatabaseInfo{
		Type: svc.Variant, Version: svc.Version, Image: svc.Image, Host: "database", Port: dialect.Port,
		Database: cfg.Database, Username: cfg.Username, HostPort: cfg.HostPort,
		VolumeName: VolumeName(view.Project.Slug, store.ServiceDatabase), State: "missing",
	}
	env := runtime.DatabaseEnv(cfg, svc.Variant)
	for k := range env {
		info.InjectedEnv = append(info.InjectedEnv, k)
	}
	sort.Strings(info.InjectedEnv)
	for _, s := range view.Status.Services {
		if s.Kind == store.ServiceDatabase {
			info.State, info.Health = s.State, s.Health
		}
	}
	if volumes, err := m.engine.ListVolumes(ctx, true); err == nil {
		for _, v := range volumes {
			if v.Name == info.VolumeName {
				info.VolumeExists = true
			}
		}
	}
	return info, nil
}

// DatabaseCredentials returns the secrets. The call is audit-logged.
func (m *Manager) DatabaseCredentials(ctx context.Context, id string) (DatabaseCredentials, error) {
	if err := validate.UUID(id); err != nil {
		return DatabaseCredentials{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	m.audit.Log(ctx, audit.ActionDBCredentialsViewed, "project", id, map[string]any{"name": p.Name})
	creds := DatabaseCredentials{
		Host: "database", Port: dialect.Port, Database: cfg.Database, Username: cfg.Username,
		Password: cfg.Password, HostPort: cfg.HostPort,
		URL: runtime.DatabaseEnv(cfg, svc.Variant)["DATABASE_URL"],
	}
	if dialect.HasRoot {
		creds.RootPassword = cfg.RootPassword
	}
	return creds, nil
}

// runSQL executes a statement as the administrator inside the database container. The
// password is passed through the environment, never on the command line. Statements are
// built only from validated identifiers and generated passwords.
func (m *Manager) runSQL(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, sql string) (string, error) {
	dialect, err := dialectOf(svc)
	if err != nil {
		return "", err
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return "", err
	}
	var db *docker.Container
	for i := range containers {
		if containers[i].Service() == string(store.ServiceDatabase) {
			db = &containers[i]
		}
	}
	if db == nil || db.State != "running" {
		return "", fmt.Errorf("%w: the database container is not running", ErrConflict)
	}
	argv, env := dialect.Client(cfg, sql)
	res, err := m.engine.Exec(ctx, db.ID, argv, env)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		return "", fmt.Errorf("database command failed: %s", sanitizeSQLError(msg, cfg))
	}
	return res.Stdout, nil
}

// sanitizeSQLError strips secrets from server error messages before they reach logs or the UI.
func sanitizeSQLError(msg string, cfg runtime.DatabaseConfig) string {
	for _, secret := range []string{cfg.RootPassword, cfg.Password} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "***")
		}
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

// ListDatabases returns the user databases on the server.
func (m *Manager) ListDatabases(ctx context.Context, id string) ([]string, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return nil, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return nil, err
	}
	out, err := m.runSQL(ctx, p, svc, cfg, dialect.ListDatabases)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || runtime.IsSystemDatabase(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// CreateDatabase creates an additional database and grants the project user access.
func (m *Manager) CreateDatabase(ctx context.Context, id, name string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := runtime.ValidateDatabaseName(name); err != nil {
		return err
	}
	unlock, err := m.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	if _, err := m.runSQL(ctx, p, svc, cfg, dialect.CreateDatabase(name, cfg.Username)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionDBCreated, "project", id, map[string]any{"name": p.Name, "database": name})
	return nil
}

// DropDatabase drops a database. confirm must equal the database name.
func (m *Manager) DropDatabase(ctx context.Context, id, name, confirm string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := runtime.ValidateDatabaseName(name); err != nil {
		return err
	}
	if confirm != name {
		return fmt.Errorf("%w: confirmation must equal the database name", validate.ErrInvalid)
	}
	unlock, err := m.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	if name == cfg.Database {
		return fmt.Errorf("%w: %q is the project's primary database; remove the database service instead", validate.ErrInvalid, name)
	}
	if _, err := m.runSQL(ctx, p, svc, cfg, dialect.DropDatabase(name)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionDBDropped, "project", id, map[string]any{"name": p.Name, "database": name})
	return nil
}

// RotateDatabasePassword sets a new password for the project user, stores it and recreates
// the application containers so they receive the new environment.
func (m *Manager) RotateDatabasePassword(ctx context.Context, id string) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return View{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return View{}, err
	}
	next, err := runtime.GeneratePassword(runtime.PasswordLength)
	if err != nil {
		return View{}, err
	}
	if _, err := m.runSQL(ctx, p, svc, cfg, dialect.AlterPassword(cfg.Username, next)); err != nil {
		return View{}, err
	}
	cfg.Password = next
	if err := m.saveDatabaseConfig(ctx, p, svc, cfg); err != nil {
		return View{}, fmt.Errorf("password changed on the server but could not be stored: %w", err)
	}
	if err := m.recreateAppContainers(ctx, id); err != nil {
		return View{}, err
	}
	m.audit.Log(ctx, audit.ActionDBPasswordRotated, "project", id, map[string]any{"name": p.Name})
	return m.Get(ctx, id)
}

// SetDatabaseExposed publishes or unpublishes the database port on the host.
func (m *Manager) SetDatabaseExposed(ctx context.Context, id string, exposed bool) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		return View{}, err
	}
	if exposed == (cfg.HostPort > 0) {
		return m.Get(ctx, id)
	}
	if exposed {
		port, err := m.allocatePort(ctx, p.HTTPPort)
		if err != nil {
			return View{}, err
		}
		cfg.HostPort = port
	} else {
		cfg.HostPort = 0
	}
	if err := m.saveDatabaseConfig(ctx, p, svc, cfg); err != nil {
		return View{}, err
	}
	if err := m.recreateContainers(ctx, id, store.ServiceDatabase); err != nil {
		return View{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"databaseHostPort": cfg.HostPort}})
	return m.Get(ctx, id)
}

// recreateAppContainers recreates every container that receives the database environment.
func (m *Manager) recreateAppContainers(ctx context.Context, id string) error {
	return m.recreateContainers(ctx, id, store.ServicePHP, store.ServiceNode)
}

// recreateContainers removes the given containers and re-applies the plan; containers are
// started again only if the project should be running. Callers hold the project lock.
func (m *Manager) recreateContainers(ctx context.Context, id string, kinds ...store.ServiceKind) error {
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	planner, err := m.planner()
	if err != nil {
		return err
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return err
	}
	existing, err := m.engine.ListContainers(ctx, true, proj.ID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[string(k)] = true
	}
	for _, c := range existing {
		if want[c.Service()] {
			if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
				return fmt.Errorf("recreate container %s: %w", c.Name, err)
			}
		}
	}
	if err := m.ensurePlan(ctx, proj, plan, proj.DesiredState == store.DesiredRunning); err != nil {
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
		return err
	}
	return nil
}

// applyDatabaseUpdate adds, changes or removes the database service of a project. Callers
// hold the project lock. It returns whether application containers must be recreated.
func (m *Manager) applyDatabaseUpdate(ctx context.Context, p store.Project, upd DatabaseUpdate, changes map[string]any) (recreateApp bool, err error) {
	svc := p.Service(store.ServiceDatabase)
	switch {
	case !upd.Enabled && svc == nil:
		return false, nil

	case !upd.Enabled && svc != nil:
		if !upd.RemoveData {
			return false, fmt.Errorf("%w: removing the database deletes its data; confirm with removeData", validate.ErrInvalid)
		}
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return false, err
		}
		for _, c := range containers {
			if c.Service() == string(store.ServiceDatabase) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return false, fmt.Errorf("remove database container: %w", err)
				}
			}
		}
		if err := m.engine.RemoveVolume(ctx, VolumeName(p.Slug, store.ServiceDatabase)); err != nil {
			return false, fmt.Errorf("remove database volume: %w", err)
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, store.ServiceDatabase); err != nil {
			return false, err
		}
		changes["database"] = "removed"
		return true, nil

	case upd.Enabled && svc == nil:
		newSvc, err := m.buildDatabaseService(p.Slug, upd.Type, upd.Version)
		if err != nil {
			return false, err
		}
		newSvc.ProjectID = p.ID
		if upd.ExposePort {
			var cfg runtime.DatabaseConfig
			_ = json.Unmarshal(newSvc.Config, &cfg)
			port, err := m.allocatePort(ctx, p.HTTPPort)
			if err != nil {
				return false, err
			}
			cfg.HostPort = port
			newSvc.Config, _ = json.Marshal(cfg)
		}
		if err := m.store.Projects.AddService(ctx, newSvc); err != nil {
			return false, err
		}
		changes["database"] = newSvc.Variant + ":" + newSvc.Version
		return true, nil

	default: // enabled and existing: version and/or port change
		dbType := upd.Type
		if dbType == "" {
			dbType = svc.Variant
		}
		if dbType != svc.Variant {
			return false, fmt.Errorf("%w: changing the database type is not supported; remove and add the database instead", validate.ErrInvalid)
		}
		version := upd.Version
		if version == "" {
			version = svc.Version
		}
		v, err := m.catalog.Resolve(dbType, version)
		if err != nil {
			return false, err
		}
		if runtime.CompareVersions(v.Version, svc.Version) < 0 {
			return false, fmt.Errorf("%w: downgrading %s from %s to %s is not supported by the data format", validate.ErrInvalid, svc.Variant, svc.Version, v.Version)
		}
		if d, _ := runtime.DialectFor(svc.Variant); !d.MajorUpgradeInPlace && v.Version != svc.Version {
			return false, fmt.Errorf("%w: %s cannot upgrade an existing data directory from %s to %s in place; export, remove and re-add the database", validate.ErrInvalid, svc.Variant, svc.Version, v.Version)
		}
		var cfg runtime.DatabaseConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return false, err
		}
		if upd.ExposePort && cfg.HostPort == 0 {
			port, err := m.allocatePort(ctx, p.HTTPPort)
			if err != nil {
				return false, err
			}
			cfg.HostPort = port
			changes["databaseHostPort"] = port
		} else if !upd.ExposePort && cfg.HostPort > 0 {
			cfg.HostPort = 0
			changes["databaseHostPort"] = 0
		}
		if v.Version != svc.Version {
			// The server rewrites its data directory on the first start with the new
			// version and cannot go back, so a dump is taken first – no dump, no upgrade.
			b, err := m.createBackupLocked(ctx, p, BackupOptions{Database: true, Note: fmt.Sprintf("before upgrading %s %s → %s", svc.Variant, svc.Version, v.Version), Source: "upgrade"})
			if err != nil {
				return false, fmt.Errorf("upgrade refused: the database must be backed up first and that failed (%w); start the project and try again", err)
			}
			changes["databaseVersion"] = v.Version
			changes["databaseBackup"] = b.ID
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return false, err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, store.ServiceDatabase, v.Version, v.Image, raw); err != nil {
			return false, err
		}
		// Port changes need a recreated database container; ensurePlan handles image changes.
		if _, changed := changes["databaseHostPort"]; changed {
			containers, err := m.engine.ListContainers(ctx, true, p.ID)
			if err != nil {
				return false, err
			}
			for _, c := range containers {
				if c.Service() == string(store.ServiceDatabase) {
					if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
						return false, fmt.Errorf("recreate database container: %w", err)
					}
				}
			}
		}
		return false, nil
	}
}

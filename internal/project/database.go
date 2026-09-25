package project

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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

// databaseConfig returns the project's primary database.
func databaseConfig(p store.Project) (*store.ProjectService, runtime.DatabaseConfig, error) {
	return databaseOf(p, "")
}

// databaseOf returns a database of the project by name: "" is the primary, anything
// else an additional one.
func databaseOf(p store.Project, db string) (*store.ProjectService, runtime.DatabaseConfig, error) {
	svc := p.Service(store.DatabaseKind(db))
	if svc == nil || !svc.Enabled {
		if db == "" {
			return nil, runtime.DatabaseConfig{}, ErrNoDatabase
		}
		return nil, runtime.DatabaseConfig{}, fmt.Errorf("%w: the project has no database %q", store.ErrNotFound, db)
	}
	var cfg runtime.DatabaseConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return nil, runtime.DatabaseConfig{}, fmt.Errorf("database config: %w", err)
	}
	return svc, cfg, nil
}

// databaseHost is the host name a database is reached at in the project network: the
// primary is "database", an additional one its name.
func databaseHost(svc *store.ProjectService) string {
	if name := svc.Kind.DatabaseName(); name != "" {
		return name
	}
	return runtime.PrimaryDatabaseHost
}

// databaseEnvPrefix is what the variables of an additional database start with:
// "analytics" → ANALYTICS_DB_HOST ("" for the primary's DB_*).
func databaseEnvPrefix(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// databaseEnv returns the variables injected for a database.
func databaseEnv(svc *store.ProjectService, cfg runtime.DatabaseConfig) map[string]string {
	return runtime.DatabaseEnvFor(cfg, svc.Variant, databaseHost(svc), databaseEnvPrefix(svc.Kind.DatabaseName()))
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
	return m.store.Projects.UpdateServiceConfig(ctx, p.ID, svc.Kind, svc.Version, svc.Image, raw)
}

// DatabaseInfo returns a database of the project ("" = the primary) without secrets.
func (m *Manager) DatabaseInfo(ctx context.Context, id, db string) (DatabaseInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return DatabaseInfo{}, err
	}
	return m.databaseInfo(ctx, view, db, nil)
}

// Databases returns every database of the project, the primary first.
func (m *Manager) Databases(ctx context.Context, id string) ([]DatabaseInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	volumes, _ := m.engine.ListVolumes(ctx, true)
	out := []DatabaseInfo{}
	for _, svc := range view.Project.Databases() {
		info, err := m.databaseInfo(ctx, view, svc.Kind.DatabaseName(), volumes)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (m *Manager) databaseInfo(ctx context.Context, view View, db string, volumes []docker.Volume) (DatabaseInfo, error) {
	svc, cfg, err := databaseOf(view.Project, db)
	if err != nil {
		return DatabaseInfo{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return DatabaseInfo{}, err
	}
	info := DatabaseInfo{
		Name: db, Service: string(svc.Kind),
		Type: svc.Variant, Version: svc.Version, Image: svc.Image, Host: databaseHost(svc), Port: dialect.Port,
		Database: cfg.Database, Username: cfg.Username, HostPort: cfg.HostPort,
		VolumeName: VolumeName(view.Project.Slug, svc.Kind), State: "missing",
	}
	env := databaseEnv(svc, cfg)
	for k := range env {
		info.InjectedEnv = append(info.InjectedEnv, k)
	}
	sort.Strings(info.InjectedEnv)
	for _, s := range view.Status.Services {
		if s.Kind == svc.Kind {
			info.State, info.Health = s.State, s.Health
		}
	}
	if volumes == nil {
		volumes, _ = m.engine.ListVolumes(ctx, true)
	}
	for _, v := range volumes {
		if v.Name == info.VolumeName {
			info.VolumeExists = true
		}
	}
	return info, nil
}

// DatabaseCredentials returns the secrets. The call is audit-logged.
func (m *Manager) DatabaseCredentials(ctx context.Context, id, db string) (DatabaseCredentials, error) {
	if err := validate.UUID(id); err != nil {
		return DatabaseCredentials{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	svc, cfg, err := databaseOf(p, db)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	m.audit.Log(ctx, audit.ActionDBCredentialsViewed, "project", id, auditDB(map[string]any{"name": p.Name}, db))
	env := databaseEnv(svc, cfg)
	creds := DatabaseCredentials{
		Host: databaseHost(svc), Port: dialect.Port, Database: cfg.Database, Username: cfg.Username,
		Password: cfg.Password, HostPort: cfg.HostPort,
		URL: env[envKey(db, "DATABASE_URL")],
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
	var c *docker.Container
	for i := range containers {
		if containers[i].Service() == string(svc.Kind) {
			c = &containers[i]
		}
	}
	if c == nil || c.State != "running" {
		return "", fmt.Errorf("%w: the database container is not running", ErrConflict)
	}
	argv, env := dialect.Client(cfg, sql)
	res, err := m.engine.Exec(ctx, c.ID, argv, env)
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
func (m *Manager) ListDatabases(ctx context.Context, id, db string) ([]string, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	svc, cfg, err := databaseOf(p, db)
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
func (m *Manager) CreateDatabase(ctx context.Context, id, db, name string) error {
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
	svc, cfg, err := databaseOf(p, db)
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
	m.audit.Log(ctx, audit.ActionDBCreated, "project", id, auditDB(map[string]any{"name": p.Name, "database": name}, db))
	return nil
}

// DropDatabase drops a database. confirm must equal the database name.
func (m *Manager) DropDatabase(ctx context.Context, id, db, name, confirm string) error {
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
	svc, cfg, err := databaseOf(p, db)
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
	m.audit.Log(ctx, audit.ActionDBDropped, "project", id, auditDB(map[string]any{"name": p.Name, "database": name}, db))
	return nil
}

// RotateDatabasePassword sets a new password for the project user, stores it and recreates
// the application containers so they receive the new environment.
func (m *Manager) RotateDatabasePassword(ctx context.Context, id, db string) (View, error) {
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
	svc, cfg, err := databaseOf(p, db)
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
	m.audit.Log(ctx, audit.ActionDBPasswordRotated, "project", id, auditDB(map[string]any{"name": p.Name}, db))
	return m.Get(ctx, id)
}

// SetDatabaseExposed publishes or unpublishes the database port on the host.
func (m *Manager) SetDatabaseExposed(ctx context.Context, id, db string, exposed bool) (View, error) {
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
	svc, cfg, err := databaseOf(p, db)
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
	if err := m.recreateContainers(ctx, id, svc.Kind); err != nil {
		return View{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, auditDB(map[string]any{"name": p.Name, "changes": map[string]any{"databaseHostPort": cfg.HostPort}}, db))
	return m.Get(ctx, id)
}

// auditDB names an additional database in audit details (the primary stays unnamed, as
// before there were several).
func auditDB(details map[string]any, db string) map[string]any {
	if db != "" {
		details["db"] = db
	}
	return details
}

// envKey is the name of a variable of a database: "DATABASE_URL" or "ANALYTICS_DATABASE_URL".
func envKey(db, key string) string {
	if db == "" {
		return key
	}
	return databaseEnvPrefix(db) + "_" + key
}

// recreateAppContainers recreates every container that receives the database environment.
func (m *Manager) recreateAppContainers(ctx context.Context, id string) error {
	return m.recreateContainers(ctx, id, store.ServicePHP, store.ServicePython, store.ServiceNode)
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

// applyDatabaseUpdate adds, changes or removes a database service of a project (kind:
// the primary or an additional one). Callers hold the project lock. It returns whether
// application containers must be recreated.
func (m *Manager) applyDatabaseUpdate(ctx context.Context, p store.Project, kind store.ServiceKind, upd DatabaseUpdate, changes map[string]any) (recreateApp bool, err error) {
	svc := p.Service(kind)
	// The keys of the audit details; an additional database's carry its name.
	key := func(k string) string {
		if name := kind.DatabaseName(); name != "" {
			return k + ":" + name
		}
		return k
	}
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
			if c.Service() == string(kind) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return false, fmt.Errorf("remove database container: %w", err)
				}
			}
		}
		if err := m.engine.RemoveVolume(ctx, VolumeName(p.Slug, kind)); err != nil {
			return false, fmt.Errorf("remove database volume: %w", err)
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return false, err
		}
		changes[key("database")] = "removed"
		return true, nil

	case upd.Enabled && svc == nil:
		newSvc, err := m.buildDatabaseService(p.Slug, upd.Type, upd.Version)
		if err != nil {
			return false, err
		}
		newSvc.ProjectID = p.ID
		newSvc.Kind = kind
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
		changes[key("database")] = newSvc.Variant + ":" + newSvc.Version
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
			changes[key("databaseHostPort")] = port
		} else if !upd.ExposePort && cfg.HostPort > 0 {
			cfg.HostPort = 0
			changes[key("databaseHostPort")] = 0
		}
		if v.Version != svc.Version {
			// The server rewrites its data directory on the first start with the new
			// version and cannot go back, so a dump is taken first – no dump, no upgrade.
			b, err := m.createBackupLocked(ctx, p, BackupOptions{Database: true, Note: fmt.Sprintf("before upgrading %s %s → %s", svc.Variant, svc.Version, v.Version), Source: "upgrade"})
			if err != nil {
				return false, fmt.Errorf("upgrade refused: the database must be backed up first and that failed (%w); start the project and try again", err)
			}
			changes[key("databaseVersion")] = v.Version
			changes[key("databaseBackup")] = b.ID
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return false, err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, kind, v.Version, v.Image, raw); err != nil {
			return false, err
		}
		// Port changes need a recreated database container; ensurePlan handles image changes.
		if _, changed := changes[key("databaseHostPort")]; changed {
			containers, err := m.engine.ListContainers(ctx, true, p.ID)
			if err != nil {
				return false, err
			}
			for _, c := range containers {
				if c.Service() == string(kind) {
					if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
						return false, fmt.Errorf("recreate database container: %w", err)
					}
				}
			}
		}
		return false, nil
	}
}

// Names an additional database cannot have: the host names of the project's other
// containers and the aliases of the primary database.
var reservedDatabaseNames = map[string]bool{
	"database": true, "db": true, "web": true, "php": true, "node": true, "python": true,
	"redis": true, "mailpit": true, "rabbitmq": true, "memcached": true, "meilisearch": true,
	"typesense": true, "opensearch": true, "opensearch-dashboards": true, "storage": true,
	"mariadb": true, "mysql": true, "postgresql": true, "postgres": true, "mongodb": true,
	"localhost": true, "worker": true, "adminer": true,
}

var databaseServiceNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// ValidateDatabaseServiceName checks the name of an additional database: it becomes a
// host name, part of container and volume names and the prefix of variables.
func ValidateDatabaseServiceName(name string) error {
	if len(name) == 0 || len(name) > 24 || !databaseServiceNameRe.MatchString(name) {
		return fmt.Errorf("%w: database name %q: 1–24 lowercase letters, digits and dashes, starting with a letter", validate.ErrInvalid, name)
	}
	if reservedDatabaseNames[name] {
		return fmt.Errorf("%w: %q is taken by another container of the project; choose another database name", validate.ErrInvalid, name)
	}
	return nil
}

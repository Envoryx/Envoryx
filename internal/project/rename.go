package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Renaming changes the one thing everything else is derived from: the slug. Host names,
// container and network names, volume names, the SSH users, the backup directory, the
// rollback image tags, the database and its login and the object storage bucket all
// follow it, and none of them can be renamed in place by Docker – containers and the
// network are recreated, volumes are copied and dropped, the database moves through a
// dump (PostgreSQL renames in place), the bucket's objects are copied into the new one.
//
// The order is chosen so that a failure leaves as little behind as possible: everything
// that can be checked is checked first, the record is renamed before any data is touched
// (and put back when the move fails), and the old data is only dropped once the new one
// is complete.

// RenameRequest is the validated intent to rename a project.
type RenameRequest struct {
	// Name is the new display name; the identifier is derived from it. Passing the
	// current name is how a rename only moves the directory.
	Name string
	// Path is the new directory below the projects root. Empty keeps the current one,
	// unless it still matches the old identifier – then it follows the new one.
	Path string
	// Confirm must equal the current identifier: renaming recreates every container and
	// rewrites the database name, so it is not something to trigger by accident.
	Confirm string
	// KeepDataNames leaves the database, its login and the bucket under their current
	// names. Everything else still moves.
	KeepDataNames bool
}

// RenameResult reports what a rename changed, for the UI and the audit log.
type RenameResult struct {
	View View
	// From and To are the old and the new identifier.
	From, To string
	// Path is the new directory (unchanged when it was not derived from the identifier).
	Path string
	// Database, Username and Bucket name what was renamed with the project; empty when
	// the project has no such service or the names were kept.
	Database string `json:",omitempty"`
	Username string `json:",omitempty"`
	Bucket   string `json:",omitempty"`
}

// Rename renames a project and everything derived from its identifier.
func (m *Manager) Rename(ctx context.Context, id string, req RenameRequest) (RenameResult, error) {
	if err := validate.UUID(id); err != nil {
		return RenameResult{}, ErrNotFound
	}
	var out RenameResult
	err := m.run(ctx, limitRename, Operation{Action: "rename", ProjectID: id}, func(ctx context.Context) (err error) {
		out, err = m.renameProject(ctx, id, req)
		return err
	})
	out.View.Status.Operation = nil
	return out, err
}

func (m *Manager) renameProject(ctx context.Context, id string, req RenameRequest) (RenameResult, error) {
	unlock, err := m.lock(id)
	if err != nil {
		return RenameResult{}, err
	}
	defer unlock()

	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return RenameResult{}, err
	}
	if proj.Lifecycle != store.LifecycleReady {
		return RenameResult{}, fmt.Errorf("%w: %s is %s and cannot be renamed right now", ErrConflict, proj.Name, proj.Lifecycle)
	}
	if req.Confirm != proj.Slug {
		return RenameResult{}, fmt.Errorf("%w: confirmation must equal the project identifier %q", validate.ErrInvalid, proj.Slug)
	}
	name, slug, path, err := renameTarget(proj, req)
	if err != nil {
		return RenameResult{}, err
	}
	if name == proj.Name && slug == proj.Slug && path == proj.Path {
		view, err := m.Get(ctx, id)
		return RenameResult{View: view, From: proj.Slug, To: proj.Slug, Path: proj.Path}, err
	}
	planner, err := m.planner()
	if err != nil {
		return RenameResult{}, err
	}
	if _, err := m.engine.Ping(ctx); err != nil {
		return RenameResult{}, err
	}
	// Refuse before anything moves: the unique indexes would catch it later, but by then
	// the containers are already stopped.
	if err := m.checkIdentityFree(ctx, proj.ID, name, slug, path); err != nil {
		return RenameResult{}, err
	}
	if err := m.checkForeignEndpoints(ctx, id); err != nil {
		return RenameResult{}, err
	}

	oldPlan, err := planner.Plan(proj)
	if err != nil {
		return RenameResult{}, err
	}
	wasRunning := proj.DesiredState == store.DesiredRunning
	result := RenameResult{From: proj.Slug, To: slug, Path: path}

	step(ctx, "Stopping the containers")
	// The share's tunnel is tied to the old container names; it ends with the rename.
	if err := m.removeShare(ctx, proj.ID); err != nil {
		return RenameResult{}, err
	}
	if err := m.stopPlan(ctx, proj, oldPlan); err != nil {
		return RenameResult{}, err
	}

	// From here on the record carries the new identity; every failure before the data has
	// moved puts it back, so the project keeps working under its old name.
	if err := m.store.Projects.UpdateIdentity(ctx, id, name, slug, path); err != nil {
		return RenameResult{}, err
	}
	restore := func(cause error, what string) (RenameResult, error) {
		ctx := context.WithoutCancel(ctx)
		if err := m.store.Projects.UpdateIdentity(ctx, id, proj.Name, proj.Slug, proj.Path); err != nil {
			cause = errors.Join(cause, fmt.Errorf("the project record could not be put back: %w", err))
		}
		// Nothing has moved yet, so the project goes back to running under its old name.
		if wasRunning {
			if err := m.startPlan(ctx, proj, oldPlan); err != nil {
				cause = errors.Join(cause, fmt.Errorf("the project could not be started again: %w", err))
			}
		}
		m.log.Error("rename failed, project keeps its old name", "project", proj.Slug, "step", what, "err", cause)
		return RenameResult{}, fmt.Errorf("%s: %w", what, opError(ctx, cause))
	}

	moved := false
	if path != proj.Path {
		step(ctx, "Moving the project directory")
		if err := moveProjectDir(planner, proj.Path, path); err != nil {
			return restore(err, "move the project directory")
		}
		moved = true
	}
	undoDir := func(cause error, what string) (RenameResult, error) {
		if moved {
			if err := moveProjectDir(planner, path, proj.Path); err != nil {
				cause = errors.Join(cause, fmt.Errorf("the project directory could not be moved back: %w", err))
			}
		}
		return restore(cause, what)
	}

	// The data names travel with the project. The old contents are only dropped once the
	// new ones are complete, so a failure here costs disk space, never data.
	renamed := proj // carries the new service configs as they are written
	renamed.Name, renamed.Slug, renamed.Path = name, slug, path
	if !req.KeepDataNames {
		// Every database of the project carries the identifier, the additional ones too.
		for _, dbSvc := range proj.Databases() {
			svc, cfg, err := databaseOf(proj, dbSvc.Kind.DatabaseName())
			if err != nil {
				return undoDir(err, "read the database configuration")
			}
			newCfg := cfg
			newCfg.Database = runtime.DBIdentifier(slug)
			newCfg.Username = newCfg.Database
			if newCfg.Database == cfg.Database && newCfg.Username == cfg.Username {
				continue
			}
			step(ctx, "Renaming the database to {{name}}", "name", newCfg.Database)
			if err := m.renameDatabase(ctx, proj, svc, cfg, newCfg); err != nil {
				return undoDir(err, "rename the database")
			}
			if err := m.saveDatabaseConfig(ctx, proj, svc, newCfg); err != nil {
				return undoDir(err, "record the database name")
			}
			setServiceConfig(&renamed, svc.Kind, newCfg)
			if svc.Kind == store.ServiceDatabase {
				result.Database, result.Username = newCfg.Database, newCfg.Username
			}
		}
		if svc, cfg, err := storageConfig(proj); err == nil {
			newCfg := cfg
			newCfg.Bucket = runtime.StorageBucketName(slug)
			if newCfg.Bucket != cfg.Bucket {
				step(ctx, "Moving the objects into the bucket {{name}}", "name", newCfg.Bucket)
				if err := m.renameBucket(ctx, proj, cfg, newCfg); err != nil {
					return undoDir(err, "rename the bucket")
				}
				raw, err := json.Marshal(newCfg)
				if err != nil {
					return undoDir(err, "record the bucket name")
				}
				if err := m.store.Projects.UpdateServiceConfig(ctx, proj.ID, store.ServiceStorage, svc.Version, svc.Image, raw); err != nil {
					return undoDir(err, "record the bucket name")
				}
				setServiceConfig(&renamed, store.ServiceStorage, newCfg)
				result.Bucket = newCfg.Bucket
			}
		}
	}

	// Past this point the data lives under the new names: going back would cost more than
	// going forward, so a failure is recorded on the project and the user retries.
	fail := func(cause error, what string) (RenameResult, error) {
		cause = opError(ctx, cause)
		msg := fmt.Sprintf("%s: %v", what, cause)
		_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, store.LifecycleReady, msg)
		m.log.Error("rename incomplete", "project", slug, "step", what, "err", cause)
		return RenameResult{}, fmt.Errorf("%s: %w", what, cause)
	}

	if err := m.moveBackups(planner, proj.Slug, slug); err != nil {
		return fail(err, "move the backups")
	}

	newPlan, err := planner.Plan(renamed)
	if err != nil {
		return fail(err, "plan the renamed project")
	}
	step(ctx, "Replacing the containers")
	if err := m.removeProjectContainers(ctx, id); err != nil {
		return fail(err, "remove the old containers")
	}
	if err := m.moveVolumes(ctx, renamed, oldPlan, newPlan); err != nil {
		return fail(err, "move the volumes")
	}
	if err := m.removeProjectNetworks(ctx, id); err != nil {
		return fail(err, "remove the old network")
	}
	if err := writePlanFiles(newPlan); err != nil {
		return fail(err, "write the configuration")
	}
	var j journal
	if failed, err := m.provision(ctx, newPlan, &j); err != nil {
		return fail(err, failed)
	}
	if wasRunning {
		if failed, err := m.startProvisioned(ctx, renamed, newPlan, j); err != nil {
			return fail(err, failed)
		}
	}
	m.retagRollbackImages(ctx, proj, slug)

	if err := m.store.Projects.UpdateState(ctx, id, proj.DesiredState, store.LifecycleReady, ""); err != nil {
		return fail(err, "finalise the rename")
	}
	m.audit.Log(ctx, audit.ActionProjectRenamed, "project", id, map[string]any{
		"name": name, "slug": slug, "path": path, "from": proj.Slug, "fromName": proj.Name,
		"database": result.Database, "bucket": result.Bucket,
	})
	view, err := m.Get(ctx, id)
	result.View = view
	return result, err
}

// renameTarget validates the request and returns the new name, identifier and directory.
func renameTarget(proj store.Project, req RenameRequest) (name, slug, path string, err error) {
	name = strings.TrimSpace(req.Name)
	if err := validate.ProjectName(name); err != nil {
		return "", "", "", err
	}
	slug = validate.Slugify(name)
	if err := validate.Slug(slug); err != nil {
		return "", "", "", fmt.Errorf("%w: project name %q does not yield a usable identifier", validate.ErrInvalid, name)
	}
	path = strings.TrimSpace(req.Path)
	if path == "" {
		// A directory that still matches the old identifier follows the new one; one the
		// user chose deliberately stays where it is.
		if proj.Path == proj.Slug {
			path = slug
		} else {
			path = proj.Path
		}
	}
	path, err = validate.RelativePath(path, 3)
	if err != nil {
		return "", "", "", err
	}
	return name, slug, path, nil
}

// checkIdentityFree reports a conflict when another project already uses one of the
// identifiers the rename wants.
func (m *Manager) checkIdentityFree(ctx context.Context, id, name, slug, path string) error {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if p.ID == id {
			continue
		}
		if strings.EqualFold(p.Name, name) || p.Slug == slug {
			return fmt.Errorf("%w: a project named %q already exists", ErrConflict, p.Name)
		}
		if p.Path == path {
			return fmt.Errorf("%w: the directory %q is already used by project %q", ErrConflict, path, p.Name)
		}
	}
	return nil
}

// setServiceConfig replaces one service's configuration on an in-memory project, so the
// plan built afterwards sees the new names.
func setServiceConfig(p *store.Project, kind store.ServiceKind, cfg any) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	for i := range p.Services {
		if p.Services[i].Kind == kind {
			p.Services[i].Config = raw
		}
	}
}

// moveProjectDir renames the project directory inside the projects root. A directory that
// is not there (never created) is not an error; an existing target is.
func moveProjectDir(planner *Planner, from, to string) error {
	root := planner.paths.ProjectsDir
	src, err := validate.ResolveUnder(root, from)
	if err != nil {
		return err
	}
	dst, err := validate.ResolveUnder(root, to)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrConflict, to)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// moveBackups takes the project's backup directory along; nothing to move is fine.
func (m *Manager) moveBackups(planner *Planner, from, to string) error {
	root := planner.paths.BackupsRoot()
	src, dst := filepath.Join(root, from), filepath.Join(root, to)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: the backup directory %s already exists", ErrConflict, to)
	}
	return os.Rename(src, dst)
}

// removeProjectContainers removes every container of a project (they are recreated from
// the new plan under their new names).
func (m *Manager) removeProjectContainers(ctx context.Context, id string) error {
	containers, err := m.engine.ListContainers(ctx, true, id)
	if err != nil {
		return err
	}
	for _, c := range containers {
		step(ctx, "Removing the container {{name}}", "name", c.Name)
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			return fmt.Errorf("remove container %s: %w", c.Name, err)
		}
	}
	return nil
}

// removeProjectNetworks removes the project's networks after the proxy and the database
// browser have left them.
func (m *Manager) removeProjectNetworks(ctx context.Context, id string) error {
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.Labels[docker.LabelProjectID] != id {
			continue
		}
		if err := m.detachProxy(ctx, n.Name); err != nil {
			return fmt.Errorf("detach proxy from %s: %w", n.Name, err)
		}
		if err := m.detachDBTool(ctx, n.Name); err != nil {
			return fmt.Errorf("detach database browser from %s: %w", n.Name, err)
		}
		if err := m.engine.RemoveNetwork(ctx, n.ID); err != nil {
			return fmt.Errorf("remove network %s: %w", n.Name, err)
		}
	}
	return nil
}

// moveVolumes copies every data volume of the old plan into the name the new plan expects
// and removes the old one. Docker cannot rename a volume, and a volume left under the old
// name would be an orphan nothing cleans up.
func (m *Manager) moveVolumes(ctx context.Context, proj store.Project, oldPlan, newPlan Plan) error {
	if len(oldPlan.Volumes) == 0 {
		return nil
	}
	web := proj.Service(store.ServiceWeb)
	if web == nil {
		return errors.New("no web image to copy the volumes with")
	}
	if len(oldPlan.Volumes) != len(newPlan.Volumes) {
		return fmt.Errorf("%w: the project has %d volumes but the renamed plan %d", ErrConflict, len(oldPlan.Volumes), len(newPlan.Volumes))
	}
	for i, from := range oldPlan.Volumes {
		to := newPlan.Volumes[i]
		if from == to {
			continue
		}
		step(ctx, "Moving the volume {{from}} to {{to}}", "from", from, "to", to)
		if err := m.copyVolume(ctx, proj, web.Image, from, to, newPlan.Labels); err != nil {
			return err
		}
		if err := m.engine.RemoveVolume(ctx, from); err != nil {
			return fmt.Errorf("remove volume %s: %w", from, err)
		}
	}
	return nil
}

// copyVolume creates the target volume and copies the source into it with a throw-away
// container from the project's own web image – the one image every project has, and small
// enough that nothing is pulled. It runs as root so ownership survives the copy.
func (m *Manager) copyVolume(ctx context.Context, proj store.Project, image, from, to string, labels map[string]string) error {
	if err := m.engine.CreateVolume(ctx, to, labels); err != nil {
		return fmt.Errorf("create volume %s: %w", to, err)
	}
	spec := docker.ContainerSpec{
		Name:       fmt.Sprintf("envoryx-%s-move-%d", proj.Slug, time.Now().UnixNano()%1_000_000),
		Image:      image,
		Labels:     docker.ManagedLabels(proj.ID, proj.Slug, "move", ""),
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "cp -a /envoryx-from/. /envoryx-to/"},
		User:       "0:0",
		Mounts: []docker.MountSpec{
			{Type: "volume", Source: from, Target: "/envoryx-from"},
			{Type: "volume", Source: to, Target: "/envoryx-to"},
		},
	}
	res, err := m.engine.RunOneShot(ctx, spec)
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	if err != nil {
		// Nothing was moved, so the half-filled target goes again.
		_ = m.engine.RemoveVolume(context.WithoutCancel(ctx), to)
		return fmt.Errorf("copy volume %s to %s: %w", from, to, err)
	}
	return nil
}

// retagRollbackImages moves the rollback tags of a project to its new identifier.
func (m *Manager) retagRollbackImages(ctx context.Context, old store.Project, slug string) {
	history, err := m.store.Images.ListByProject(ctx, old.ID)
	if err != nil {
		return
	}
	for _, rec := range history {
		m.protectRollbackTarget(ctx, slug, rec)
		m.releaseRollbackTarget(ctx, old.Slug, rec.Image)
	}
}

// renameDatabase moves the project's database and its login to the new names. PostgreSQL
// renames both in place; everywhere else the contents travel through a dump into a freshly
// created database, and the old one is dropped once that succeeded.
func (m *Manager) renameDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, from, to runtime.DatabaseConfig) error {
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	return m.withServiceRunning(ctx, p, svc.Kind, func(ctx context.Context) error {
		if err := m.waitForDatabase(ctx, p, svc, from, dialect); err != nil {
			return err
		}
		// The login first: everything after it authenticates as the new user. The
		// password never changes, so only the name moves.
		cfg := from
		if from.Username != to.Username && dialect.RenameUser != nil {
			admin := cfg
			if dialect.HelperLogin != nil {
				helper, err := m.helperLogin(ctx, p, svc, cfg, dialect)
				if err != nil {
					return err
				}
				admin = helper
			}
			_, err := m.runSQL(ctx, p, svc, admin, dialect.RenameUser(from.Username, to.Username, from))
			if err == nil {
				cfg.Username = to.Username
			}
			if dialect.DropLogin != nil && admin.Username != cfg.Username {
				// Removed as whoever the project's login is now, whatever the rename did.
				if _, derr := m.runSQL(context.WithoutCancel(ctx), p, svc, cfg, dialect.DropLogin(admin.Username)); derr != nil {
					m.log.Warn("the helper login of a rename was not removed", "project", p.Slug, "login", admin.Username, "err", derr)
				}
			}
			if err != nil {
				return fmt.Errorf("rename the login: %w", err)
			}
		}
		if from.Database == to.Database {
			return nil
		}
		if dialect.RenameDatabase != nil {
			if _, err := m.runSQL(ctx, p, svc, cfg, dialect.RenameDatabase(from.Database, to.Database)); err != nil {
				return fmt.Errorf("rename the database: %w", err)
			}
			return nil
		}
		if _, err := m.runSQL(ctx, p, svc, cfg, dialect.CreateDatabase(to.Database, cfg.Username)); err != nil {
			return fmt.Errorf("create the new database: %w", err)
		}
		c, err := m.ServiceContainer(ctx, p.ID, svc.Kind)
		if err != nil {
			return err
		}
		fromCfg, toCfg := cfg, cfg
		toCfg.Database = to.Database
		if err := m.streamDump(ctx, dialect, c.ID, fromCfg, c.ID, toCfg); err != nil {
			return fmt.Errorf("move the contents: %w", err)
		}
		if _, err := m.runSQL(ctx, p, svc, cfg, dialect.DropDatabase(from.Database)); err != nil {
			return fmt.Errorf("drop the old database: %w", err)
		}
		return nil
	})
}

// helperLogin creates the short-lived administrator of a rename (see Dialect.HelperLogin)
// and returns the configuration that logs in as it.
func (m *Manager) helperLogin(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, dialect runtime.Dialect) (runtime.DatabaseConfig, error) {
	pw, err := runtime.GeneratePassword(runtime.PasswordLength)
	if err != nil {
		return runtime.DatabaseConfig{}, err
	}
	helper := cfg
	helper.Username, helper.Password = "envoryx_rename", pw
	// A helper a crash left behind goes first; its password is unknown.
	if _, err := m.runSQL(ctx, p, svc, cfg, dialect.DropLogin(helper.Username)+"; "+dialect.HelperLogin(helper.Username, pw)); err != nil {
		return runtime.DatabaseConfig{}, fmt.Errorf("create a helper login for the rename: %w", err)
	}
	return helper, nil
}

// renameBucket copies the objects into a bucket under the new name and removes the old
// one once they have all arrived.
func (m *Manager) renameBucket(ctx context.Context, p store.Project, from, to runtime.StorageConfig) error {
	return m.withServiceRunning(ctx, p, store.ServiceStorage, func(ctx context.Context) error {
		if err := m.provisionBucket(ctx, p, to); err != nil {
			return err
		}
		st, _, err := m.storageStore(ctx, p)
		if err != nil {
			return err
		}
		if err := copyObjects(ctx, st, from.Bucket, st, to.Bucket); err != nil {
			return err
		}
		objects, err := st.ListObjects(ctx, from.Bucket)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		keys := make([]string, 0, len(objects))
		for _, o := range objects {
			keys = append(keys, o.Key)
		}
		if len(keys) > 0 {
			if err := st.DeleteObjects(ctx, from.Bucket, keys); err != nil {
				return fmt.Errorf("empty the old bucket: %w", err)
			}
		}
		if err := st.DeleteBucket(ctx, from.Bucket); err != nil {
			return fmt.Errorf("remove the old bucket: %w", err)
		}
		return nil
	})
}

package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Duplicating is the create path with a different source of truth: the desired state does not
// come from a wizard request but from a project that already exists. Only the things that
// must be unique change – name, slug, directory, container identities and every published
// host port. Database and object-storage credentials are copied verbatim on purpose: the
// copy has its own container, network and volume, so nothing is shared, while a checked-in
// .env of the original keeps working and the dump restores one to one (a renamed database
// would need --nsFrom/--nsTo for MongoDB and rewritten grants for the others).
//
// Backup schedules and extra domains are never copied: host names are unique, and a copy
// made to try something out should not inherit the original's scheduled backups.

// DuplicateRequest is the validated intent to duplicate a project.
type DuplicateRequest struct {
	// Name of the copy; its slug and, unless Path says otherwise, its directory.
	Name string
	// Path is the copy's directory relative to the projects root; empty = the slug.
	Path string
	// Files copies the project directory, IncludeDependencies with vendor/, node_modules/
	// and the other regenerable directories the file backup also skips.
	Files               bool
	IncludeDependencies bool
	// Database copies the contents of the primary database, Storage the objects of the
	// bucket. Both are ignored when the original has no such service.
	Database bool
	Storage  bool
	// Workers copies the worker and cron job definitions, Git the repository binding.
	Workers bool
	Git     bool
	// Start starts the copy when it is ready.
	Start bool
}

// databaseReadyTimeout bounds the wait for a freshly created database container: it
// initialises its data directory on the first start, which takes seconds, not minutes.
const databaseReadyTimeout = 3 * time.Minute

// Duplicate copies a project. Like Create it runs detached from ctx's cancellation and
// rolls everything back when a step fails. (Manager.Clone is the git clone; the copy of a
// whole project is called a duplicate throughout, endpoint and CLI included.)
func (m *Manager) Duplicate(ctx context.Context, id string, req DuplicateRequest) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	return m.runView(ctx, limitDuplicate, Operation{Action: "duplicate", ProjectSlug: validate.Slugify(req.Name), ProjectName: req.Name}, func(ctx context.Context) (View, error) {
		return m.duplicate(ctx, id, req)
	})
}

func (m *Manager) duplicate(ctx context.Context, id string, req DuplicateRequest) (View, error) {
	src, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	if src.Lifecycle != store.LifecycleReady {
		return View{}, fmt.Errorf("%w: %s is %s and cannot be copied right now", ErrConflict, src.Name, src.Lifecycle)
	}
	proj, err := duplicateProject(src, req)
	if err != nil {
		return View{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return View{}, err
	}
	if _, err := m.engine.Ping(ctx); err != nil {
		return View{}, err
	}

	// The original is read while the copy is built – files, a dump, the bucket – so it is
	// locked for the duration: a start, restore or update in between would copy half of
	// one state and half of another.
	unlockSrc, err := m.lock(src.ID)
	if err != nil {
		return View{}, err
	}
	defer unlockSrc()
	unlock, err := m.lock(proj.ID)
	if err != nil {
		return View{}, err
	}
	defer unlock()

	// Ports and insert under one lock, as in create: two copies started at the same time
	// must not pick the same port.
	m.createMu.Lock()
	port, err := m.allocatePort(ctx)
	if err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	proj.HTTPPort = port
	if err := m.reassignHostPorts(ctx, &proj); err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	if err := m.store.Projects.Create(ctx, &proj); err != nil {
		m.createMu.Unlock()
		return View{}, err
	}
	m.createMu.Unlock()
	setProject(ctx, proj.ID)

	var j journal
	fail := func(step string, cause error) (View, error) {
		cause = opError(ctx, cause)
		m.log.Error("duplicating a project failed, rolling back", "project", proj.Slug, "source", src.Slug, "step", step, "err", cause)
		rbErr := m.rollback(ctx, j)
		if delErr := m.store.Projects.Delete(context.WithoutCancel(ctx), proj.ID); delErr != nil {
			rbErr = errors.Join(rbErr, delErr)
		}
		m.audit.Log(ctx, audit.ActionProjectFailed, "project", proj.ID, map[string]any{"name": proj.Name, "duplicateOf": src.Name, "step": step, "error": cause.Error()})
		m.notify(ctx, notify.Event{Kind: "project.failed", Level: notify.Error, Project: proj.Name, Title: fmt.Sprintf("Copying %s failed", src.Name), Message: fmt.Sprintf("Step %q failed: %v. The copy was rolled back.", step, cause)})
		if rbErr != nil {
			return View{}, fmt.Errorf("%s: %w (rollback incomplete: %v)", step, cause, rbErr)
		}
		return View{}, fmt.Errorf("%s: %w", step, cause)
	}

	for i := range proj.Workers {
		w := &proj.Workers[i]
		w.ProjectID = proj.ID
		if err := m.store.Workers.Add(ctx, w); err != nil {
			return fail("copy worker "+w.Name, err)
		}
	}
	if req.Workers {
		jobs, err := m.store.CronJobs.ListByProject(ctx, src.ID)
		if err != nil {
			return fail("copy cron jobs", err)
		}
		for _, j := range jobs {
			j.ID, j.ProjectID, j.LastRun = "", proj.ID, nil
			if err := m.store.CronJobs.Add(ctx, &j); err != nil {
				return fail("copy cron job "+j.Name, err)
			}
		}
	}

	plan, err := planner.Plan(proj)
	if err != nil {
		return fail("plan the copy", err)
	}
	j.configDir = plan.ConfigDir

	target := planner.ProjectDir(proj)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		// Only a directory this operation created is removed again when it fails.
		j.projectDir = target
	} else if err == nil && req.Files {
		entries, rerr := os.ReadDir(target)
		if rerr != nil {
			return fail("prepare project directory", rerr)
		}
		if len(entries) > 0 {
			return fail("prepare project directory", fmt.Errorf("%w: %s already exists and is not empty", ErrConflict, filepath.Join(proj.Path)))
		}
	}
	step(ctx, "Preparing the project directory")
	if err := m.ensureProjectDir(planner, proj, false, req.Files); err != nil {
		return fail("prepare project directory", err)
	}
	if req.Files {
		step(ctx, "Copying the project files")
		if err := m.copyProjectFiles(planner, src, proj, req.IncludeDependencies); err != nil {
			return fail("copy project files", err)
		}
	}
	step(ctx, "Writing the configuration")
	if err := writePlanFiles(plan); err != nil {
		return fail("write configuration", err)
	}
	if failed, err := m.provision(ctx, plan, &j); err != nil {
		return fail(failed, err)
	}
	if req.Start {
		if failed, err := m.startProvisioned(ctx, proj, plan, j); err != nil {
			return fail(failed, err)
		}
	}
	if req.Database {
		for _, svc := range proj.Databases() {
			db := svc.Kind.DatabaseName()
			_, cfg, err := databaseOf(proj, db)
			if err != nil {
				return fail("copy the database", err)
			}
			label := cfg.Database
			if db != "" {
				label = db
			}
			step(ctx, "Copying the database {{name}}", "name", label)
			if err := m.copyDatabaseOf(ctx, src, db, proj, db); err != nil {
				return fail("copy the database "+label, err)
			}
		}
	}
	if req.Storage {
		if _, _, err := storageConfig(proj); err == nil {
			step(ctx, "Copying the objects of the bucket")
			if err := m.copyStorage(ctx, src, proj); err != nil {
				return fail("copy the object storage", err)
			}
		}
	}
	step(ctx, "Finishing up")
	if err := m.store.Projects.UpdateState(ctx, proj.ID, proj.DesiredState, store.LifecycleReady, ""); err != nil {
		return fail("finalise the copy", err)
	}
	m.audit.Log(ctx, audit.ActionProjectDuplicated, "project", proj.ID, map[string]any{
		"name": proj.Name, "slug": proj.Slug, "path": proj.Path, "port": proj.HTTPPort,
		"source": src.Name, "sourceId": src.ID,
		"files": req.Files, "database": req.Database, "storage": req.Storage, "workers": req.Workers,
	})
	return m.Get(ctx, proj.ID)
}

// duplicateProject derives the desired state of the copy from the original.
func duplicateProject(src store.Project, req DuplicateRequest) (store.Project, error) {
	if err := validate.ProjectName(req.Name); err != nil {
		return store.Project{}, err
	}
	slug := validate.Slugify(req.Name)
	if err := validate.Slug(slug); err != nil {
		return store.Project{}, fmt.Errorf("%w: project name %q does not yield a usable identifier", validate.ErrInvalid, req.Name)
	}
	if slug == src.Slug {
		return store.Project{}, fmt.Errorf("%w: the copy needs a name of its own", validate.ErrInvalid)
	}
	relPath := req.Path
	if strings.TrimSpace(relPath) == "" {
		relPath = slug
	}
	relPath, err := validate.RelativePath(relPath, 3)
	if err != nil {
		return store.Project{}, err
	}
	if relPath == src.Path {
		return store.Project{}, fmt.Errorf("%w: the copy needs a directory of its own", validate.ErrInvalid)
	}

	dst := store.Project{
		ID:           store.NewID(),
		Name:         req.Name,
		Slug:         slug,
		Path:         relPath,
		Docroot:      src.Docroot,
		DesiredState: store.DesiredStopped,
		Lifecycle:    store.LifecycleCreating,
		IDEGateway:   src.IDEGateway,
		ProxyRules:   src.ProxyRules,
	}
	if req.Start {
		dst.DesiredState = store.DesiredRunning
	}
	if req.Git {
		dst.Git = src.Git
	}
	for _, s := range src.Services {
		config := slices.Clone(s.Config)
		if externalService(&s) {
			// The copy gets a server of its own – for a database with the original's
			// data, copied in later – so nothing done to it ever reaches the external one.
			config = json.RawMessage(`{"hostPort":0}`)
			if s.Kind.IsDatabase() {
				cfg, err := runtime.NewDatabaseConfig(slug)
				if err != nil {
					return store.Project{}, err
				}
				if config, err = json.Marshal(cfg); err != nil {
					return store.Project{}, err
				}
			}
		}
		dst.Services = append(dst.Services, store.ProjectService{
			Kind: s.Kind, Variant: s.Variant, Version: s.Version, Image: s.Image, Enabled: s.Enabled,
			Config: config, Position: s.Position,
		})
	}
	for _, e := range src.Env {
		dst.Env = append(dst.Env, store.EnvVar{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	if req.Workers {
		for _, w := range src.Workers {
			dst.Workers = append(dst.Workers, store.Worker{Name: w.Name, Preset: w.Preset, Args: slices.Clone(w.Args), Enabled: w.Enabled, Position: w.Position})
		}
	}
	return dst, nil
}

// editConfig decodes a service configuration, lets fn change it and stores it back.
func editConfig[T any](svc *store.ProjectService, fn func(*T) error) error {
	var cfg T
	if len(svc.Config) > 0 {
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	svc.Config = raw
	return nil
}

// reassignHostPorts gives the copy host ports of its own wherever the original published
// one. Which ports a service has is part of its configuration, so every kind is decoded,
// its non-zero ports are replaced and it is encoded again.
func (m *Manager) reassignHostPorts(ctx context.Context, proj *store.Project) error {
	taken := []int{proj.HTTPPort}
	swap := func(port *int) error {
		if *port == 0 {
			return nil
		}
		p, err := m.allocatePort(ctx, taken...)
		if err != nil {
			return err
		}
		taken = append(taken, p)
		*port = p
		return nil
	}
	for i := range proj.Services {
		svc := &proj.Services[i]
		var err error
		switch {
		case svc.Kind.IsDatabase():
			err = editConfig(svc, func(c *runtime.DatabaseConfig) error { return swap(&c.HostPort) })
		case svc.Kind == store.ServiceNode:
			err = editConfig(svc, func(c *runtime.NodeConfig) error {
				if err := swap(&c.HostPort); err != nil {
					return err
				}
				return swap(&c.InspectHostPort)
			})
		case svc.Kind == store.ServicePython:
			err = editConfig(svc, func(c *runtime.PythonConfig) error {
				if err := swap(&c.HostPort); err != nil {
					return err
				}
				return swap(&c.DebugHostPort)
			})
		case svc.Kind == store.ServiceGo:
			err = editConfig(svc, func(c *runtime.GoConfig) error {
				if err := swap(&c.HostPort); err != nil {
					return err
				}
				return swap(&c.DebugHostPort)
			})
		case svc.Kind == store.ServiceStorage:
			err = editConfig(svc, func(c *runtime.StorageConfig) error {
				if err := swap(&c.HostPort); err != nil {
					return err
				}
				return swap(&c.ConsolePort)
			})
		case slices.Contains([]store.ServiceKind{store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama}, svc.Kind):
			err = editConfig(svc, func(c *runtime.ServiceConfig) error {
				if err := swap(&c.HostPort); err != nil {
					return err
				}
				return swap(&c.WebUIPort)
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// copyProjectFiles copies the original's directory into the copy's, refusing to start
// when the filesystem does not have room for it.
func (m *Manager) copyProjectFiles(planner *Planner, src, dst store.Project, deps bool) error {
	from, to := planner.ProjectDir(src), planner.ProjectDir(dst)
	if err := disk.Require(to, uint64(dirSizeSkipping(from, !deps))); err != nil {
		return err
	}
	return copyTree(from, to, !deps, planner.paths.PUID, planner.paths.PGID)
}

// copyTree copies a directory recursively. Directories, regular files and symlinks are
// copied (links relative to the tree stay links, as in the file backup); sockets, devices
// and pipes are skipped, and everything created belongs to the project user.
func copyTree(src, dst string, skipDeps bool, uid, gid int) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	chown := func(path string) {
		if os.Geteuid() == 0 {
			_ = os.Lchown(path, uid, gid)
		}
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && skipDeps && dependencyDirs[d.Name()] {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyFile(path, target, info.Mode().Perm()|0o600); err != nil {
				return err
			}
		default:
			return nil // sockets, devices, pipes: nothing to copy
		}
		chown(target)
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// withServiceRunning makes sure a service container runs while fn works with it and
// leaves it as it found it: a copy into a project that is not started (or out of one)
// starts the container for the transfer and stops it again afterwards.
func (m *Manager) withServiceRunning(ctx context.Context, p store.Project, kind store.ServiceKind, fn func(context.Context) error) error {
	if externalService(p.Service(kind)) {
		return fn(ctx) // nothing of ours to start: the server runs elsewhere
	}
	c, err := m.ServiceContainer(ctx, p.ID, kind)
	if err != nil {
		return err
	}
	// The container list can still say "running" for a moment after a stop returned
	// (a rename stops the project right before this); the container itself knows.
	running := c.State == "running"
	if d, err := m.engine.InspectContainer(ctx, c.ID); err == nil {
		running = d.Running
	}
	if running {
		return fn(ctx)
	}
	step(ctx, "Starting the container {{name}}", "name", c.Name)
	if err := m.engine.StartContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("start %s: %w", c.Name, err)
	}
	err = fn(ctx)
	// The container is stopped again whatever fn returned, and also when the operation
	// was cancelled meanwhile.
	if serr := m.engine.StopContainer(context.WithoutCancel(ctx), c.ID, m.cfg.StopTimeout); serr != nil && err == nil {
		err = fmt.Errorf("stop %s: %w", c.Name, serr)
	}
	return err
}

// copyDatabase transfers the contents of the original's primary database into the copy's.
func (m *Manager) copyDatabase(ctx context.Context, src, dst store.Project) error {
	return m.copyDatabaseOf(ctx, src, "", dst, "")
}

// copyDatabaseOf transfers the contents of a database of one project into a database of
// another (or the same) project. A source without that database has nothing to copy.
func (m *Manager) copyDatabaseOf(ctx context.Context, src store.Project, srcDB string, dst store.Project, dstDB string) error {
	srcSvc, srcCfg, err := databaseOf(src, srcDB)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // the original has none: nothing to copy
		}
		return err
	}
	dstSvc, dstCfg, err := databaseOf(dst, dstDB)
	if err != nil {
		return err
	}
	dialect, err := dialectOf(dstSvc)
	if err != nil {
		return err
	}
	if srcSvc.Variant != dstSvc.Variant {
		return fmt.Errorf("%w: the source runs %s, the target %s", ErrConflict, srcSvc.Variant, dstSvc.Variant)
	}
	return m.withServiceRunning(ctx, src, srcSvc.Kind, func(ctx context.Context) error {
		return m.withServiceRunning(ctx, dst, dstSvc.Kind, func(ctx context.Context) error {
			if err := m.waitForDatabase(ctx, src, srcSvc, srcCfg, dialect); err != nil {
				return err
			}
			if err := m.waitForDatabase(ctx, dst, dstSvc, dstCfg, dialect); err != nil {
				return err
			}
			return m.streamDump(ctx, dialect, dbEnd{src, srcSvc, srcCfg}, dbEnd{dst, dstSvc, dstCfg})
		})
	})
}

// streamDump pipes a logical dump of one database straight into the client of another –
// no temporary file, and for a project of a few hundred megabytes it is over in seconds.
// The two ends are different databases when a project is copied and the same server
// when one is renamed, and either may be an external one; the payload is taken as it is
// unless the dialect has to map the database name itself (MongoDB's archive carries its
// namespace).
func (m *Manager) streamDump(ctx context.Context, dialect runtime.Dialect, from, to dbEnd) error {
	fromCfg, toCfg := from.cfg, to.cfg
	pr, pw := io.Pipe()
	dumped := make(chan error, 1)
	go func() {
		var stderr strings.Builder
		argv, env := dialect.Dump(fromCfg)
		code, err := m.dbStream(ctx, from, argv, env, nil, pw, &limitedBuilder{b: &stderr})
		if err == nil && code != 0 {
			err = fmt.Errorf("dump failed (exit %d): %s", code, sanitizeSQLError(strings.TrimSpace(stderr.String()), fromCfg))
		}
		_ = pw.CloseWithError(err)
		dumped <- err
	}()
	argv, env := dialect.Restore(toCfg)
	if dialect.RestoreInto != nil {
		argv, env = dialect.RestoreInto(toCfg, fromCfg.Database)
	}
	var stderr strings.Builder
	code, err := m.dbStream(ctx, to, argv, env, pr, nil, &limitedBuilder{b: &stderr})
	// Unblock the dump when the import stopped reading, then wait for it either way.
	_ = pr.CloseWithError(err)
	if derr := <-dumped; derr != nil {
		return derr
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("import failed (exit %d): %s", code, sanitizeSQLError(strings.TrimSpace(stderr.String()), toCfg))
	}
	return nil
}

// waitForDatabase waits until a database container serves its port and accepts the
// project's credentials. A container created moments ago is still initialising its data
// directory, and during that the server answers on the socket only – the health command
// is therefore asked first, and only then the client.
func (m *Manager) waitForDatabase(ctx context.Context, p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig, dialect runtime.Dialect) error {
	if cfg.External() {
		// Nobody is starting it for us: it answers now or it is not reachable.
		if _, err := m.runSQL(ctx, p, svc, cfg, dialect.ListDatabases); err != nil {
			return externalUnreachable(cfg, err)
		}
		return nil
	}
	c, err := m.ServiceContainer(ctx, p.ID, svc.Kind)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(databaseReadyTimeout)
	wait := func(probe func() error) error {
		reported := false
		for {
			err := probe()
			if err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("the database of %s did not become ready in time: %w%s", p.Slug, err, m.containerTail(ctx, c.ID))
			}
			if !reported {
				step(ctx, "Waiting for the database of {{project}}", "project", p.Slug)
				reported = true
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if len(dialect.Health) > 0 {
		if err := wait(func() error {
			res, err := m.engine.Exec(ctx, c.ID, dialect.Health, nil)
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				return fmt.Errorf("%s is not serving yet", c.Name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return wait(func() error {
		_, err := m.runSQL(ctx, p, svc, cfg, dialect.ListDatabases)
		return err
	})
}

// containerTail returns the last lines a container wrote, for an error that says why it
// is not serving ("" when there is nothing to show).
func (m *Manager) containerTail(ctx context.Context, id string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var lines []string
	_ = m.engine.StreamLogs(ctx, id, docker.LogOptions{Tail: "15"}, func(l docker.LogLine) {
		lines = append(lines, strings.TrimRight(l.Text, "\r\n"))
	})
	if len(lines) == 0 {
		return ""
	}
	return "\nlast output of the container:\n" + strings.Join(lines, "\n")
}

// copyStorage uploads every object of the original's bucket into the copy's.
func (m *Manager) copyStorage(ctx context.Context, src, dst store.Project) error {
	if _, _, err := storageConfig(src); err != nil {
		return nil // the original has no object storage: nothing to copy
	}
	_, dstCfg, err := storageConfig(dst)
	if err != nil {
		return err
	}
	return m.withServiceRunning(ctx, src, store.ServiceStorage, func(ctx context.Context) error {
		return m.withServiceRunning(ctx, dst, store.ServiceStorage, func(ctx context.Context) error {
			// The bucket exists only after the project has been started once.
			if err := m.provisionBucket(ctx, dst, dstCfg); err != nil {
				return err
			}
			fromStore, fromCfg, err := m.storageStore(ctx, src)
			if err != nil {
				return err
			}
			toStore, toCfg, err := m.storageStore(ctx, dst)
			if err != nil {
				return err
			}
			return copyObjects(ctx, fromStore, fromCfg.Bucket, toStore, toCfg.Bucket)
		})
	})
}

// copyObjects reads every object of one bucket and writes it into another, keeping the
// content type. Both ends may be the same server (a renamed bucket).
func copyObjects(ctx context.Context, from s3.ObjectStore, fromBucket string, to s3.ObjectStore, toBucket string) error {
	objects, err := from.ListObjects(ctx, fromBucket)
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	for _, o := range objects {
		body, ctype, err := from.GetObject(ctx, fromBucket, o.Key)
		if err != nil {
			return fmt.Errorf("get %s: %w", o.Key, err)
		}
		err = to.PutObject(ctx, toBucket, o.Key, body, o.Size, ctype)
		body.Close()
		if err != nil {
			return fmt.Errorf("upload %s: %w", o.Key, err)
		}
	}
	return nil
}

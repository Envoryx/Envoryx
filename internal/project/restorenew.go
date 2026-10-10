package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Restoring a backup into a new project is the duplicate path with the backup as the
// original: the desired state comes from the project export the backup carries (sealed in
// backup.json), the data from its dumps and archives. It works for the backups of a
// project that still exists and for those of a deleted one, whose records and directories
// stay behind when the project goes.

// RestoreNewRequest is the validated intent to restore a backup into a new project.
type RestoreNewRequest struct {
	// Name of the new project; its slug and, unless Path says otherwise, its directory.
	Name string
	// Path is the new project's directory relative to the projects root; empty means the
	// slug.
	Path string
	// Database restores the dumps (and the addon volumes), Files the project files,
	// Storage the objects of the bucket. Each is ignored when the backup holds no such part.
	Database bool
	Files    bool
	Storage  bool
	// Start starts the new project when it is ready.
	Start bool
}

// OrphanedBackup is a backup of a project that no longer exists.
type OrphanedBackup struct {
	ProjectID   string     `json:"projectId"`
	ProjectName string     `json:"projectName"`
	Slug        string     `json:"slug"`
	Backup      BackupInfo `json:"backup"`
}

// OrphanedBackups lists the backups of deleted projects, newest first.
func (m *Manager) OrphanedBackups(ctx context.Context) ([]OrphanedBackup, error) {
	rows, err := m.store.Backups.ListOrphaned(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OrphanedBackup, 0, len(rows))
	for _, b := range rows {
		info := BackupInfo{ID: b.ID, Dir: b.Filename, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt}
		_ = json.Unmarshal(b.Metadata, &info.Meta)
		if _, err := m.locateBackup(ctx, b, info.Meta); err != nil {
			info.Missing = true
		}
		out = append(out, OrphanedBackup{ProjectID: b.ProjectID, ProjectName: info.Meta.ProjectName, Slug: info.Meta.Slug, Backup: info})
	}
	return out, nil
}

// DeleteOrphanedBackup removes a backup of a deleted project, its files included.
func (m *Manager) DeleteOrphanedBackup(ctx context.Context, projectID, backupID string) error {
	if validate.UUID(projectID) != nil || validate.UUID(backupID) != nil {
		return ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, projectID); err == nil {
		return fmt.Errorf("%w: the project still exists; delete the backup there", ErrConflict)
	}
	b, err := m.store.Backups.Get(ctx, projectID, backupID)
	if err != nil {
		return err
	}
	var meta BackupMeta
	_ = json.Unmarshal(b.Metadata, &meta)
	if dir, err := m.locateBackup(ctx, b, meta); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove backup files: %w", err)
		}
	}
	if err := m.store.Backups.Delete(ctx, projectID, backupID); err != nil {
		return err
	}
	_ = m.store.Offsite.DeleteByBackup(ctx, store.OffsiteProject, backupID)
	m.audit.Log(ctx, audit.ActionBackupDeleted, "project", projectID, map[string]any{"name": meta.ProjectName, "backup": backupID})
	return nil
}

// locateBackup finds the directory of a backup. It lives below the backup root in a
// directory named after the project's slug: the current one for a project that exists,
// the one the backup was made under for a deleted project, and, when that project was
// renamed in between (the rename moves its backups), whichever directory holds a backup
// of that name made for that project.
func (m *Manager) locateBackup(ctx context.Context, b store.Backup, meta BackupMeta) (string, error) {
	isIt := func(dir string) bool {
		raw, err := os.ReadFile(filepath.Join(dir, backupMetaFile))
		if err != nil {
			return false
		}
		var bm BackupMeta
		return json.Unmarshal(raw, &bm) == nil && bm.ProjectID == b.ProjectID
	}
	slugs := []string{}
	if p, err := m.store.Projects.Get(ctx, b.ProjectID); err == nil {
		slugs = append(slugs, p.Slug)
	}
	if meta.Slug != "" {
		slugs = append(slugs, meta.Slug)
	}
	for _, slug := range slugs {
		if dir, err := m.backupDir(slug, b.Filename); err == nil && isIt(dir) {
			return dir, nil
		}
	}
	paths, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	entries, _ := os.ReadDir(paths.BackupsRoot())
	for _, e := range entries {
		if !e.IsDir() || validate.Slug(e.Name()) != nil {
			continue
		}
		if dir, err := m.backupDir(e.Name(), b.Filename); err == nil && isIt(dir) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: the files of this backup are gone", ErrNotFound)
}

// readExport reads the project export of the backup in dir.
func readExport(dir string) (projectExport, error) {
	raw, err := os.ReadFile(filepath.Join(dir, backupMetaFile))
	if err != nil {
		return projectExport{}, err
	}
	var bf backupFile
	if err := json.Unmarshal(raw, &bf); err != nil {
		return projectExport{}, fmt.Errorf("read %s: %w", backupMetaFile, err)
	}
	return bf.export()
}

// export returns the project export of backup.json, unsealed.
func (bf backupFile) export() (projectExport, error) {
	switch {
	case bf.SealedExport != "":
		plain, err := secrets.Open(bf.SealedExport)
		if errors.Is(err, secrets.ErrUnknownKey) {
			return projectExport{}, fmt.Errorf("%w: the backup's project settings are sealed with the secret key of another Envoryx; start this one with that key (ENVORYX_SECRET_KEY or /config/secret.key)", validate.ErrInvalid)
		}
		if err != nil {
			return projectExport{}, fmt.Errorf("%w: the backup's project settings can't be opened: %v", validate.ErrInvalid, err)
		}
		var ex projectExport
		if err := json.Unmarshal([]byte(plain), &ex); err != nil {
			return projectExport{}, fmt.Errorf("%w: the backup's project settings are unreadable: %v", validate.ErrInvalid, err)
		}
		return ex, nil
	case bf.Export != nil:
		return *bf.Export, nil
	}
	return projectExport{}, fmt.Errorf("%w: this backup holds no project settings", validate.ErrInvalid)
}

// projectFromExport turns an export back into a project, the original a duplicate is
// made from. It has no identity of its own: neither the ID nor slug or directory of the
// original count, since it may be gone (and the new project may take its name).
func projectFromExport(ex projectExport) store.Project {
	p := store.Project{
		Name: ex.Name, Docroot: ex.Docroot, IDEGateway: ex.IDEGateway,
		Git: store.GitConfig{URL: ex.Git.URL, Branch: ex.Git.Branch, Username: ex.Git.Username, Token: ex.Git.Token},
	}
	if ex.Limits != nil {
		p.Limits = *ex.Limits
	}
	if ex.HealthCheck != nil {
		p.HealthCheck = *ex.HealthCheck
	}
	if ex.ProxyRules != nil {
		p.ProxyRules = *ex.ProxyRules
	}
	for i, s := range ex.Services {
		svc := store.ProjectService{Kind: store.ServiceKind(s.Kind), Variant: s.Variant, Version: s.Version, Config: s.Config, Enabled: true, Position: s.Position}
		if s.Position == 0 {
			svc.Position = i
		}
		if s.Custom != nil {
			svc.Custom = *s.Custom
		}
		p.Services = append(p.Services, svc)
	}
	for _, e := range ex.Env {
		p.Env = append(p.Env, store.EnvVar{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	for i, w := range ex.Workers {
		p.Workers = append(p.Workers, store.Worker{Name: w.Name, Preset: w.Preset, Args: w.Args, Enabled: w.Enabled, Position: i})
	}
	return p
}

// RestoreIntoNewProject creates a project from a backup: the settings it was made with
// and, as asked, its databases, files and objects. Like Duplicate it runs detached from
// ctx's cancellation and rolls everything back when a step fails.
func (m *Manager) RestoreIntoNewProject(ctx context.Context, projectID, backupID string, req RestoreNewRequest) (View, error) {
	if validate.UUID(projectID) != nil || validate.UUID(backupID) != nil {
		return View{}, ErrNotFound
	}
	return m.runView(ctx, limitDuplicate, Operation{Action: "restore-new", ProjectSlug: validate.Slugify(req.Name), ProjectName: req.Name}, func(ctx context.Context) (View, error) {
		return m.restoreIntoNew(ctx, projectID, backupID, req)
	})
}

func (m *Manager) restoreIntoNew(ctx context.Context, projectID, backupID string, req RestoreNewRequest) (View, error) {
	b, err := m.store.Backups.Get(ctx, projectID, backupID)
	if err != nil {
		return View{}, err
	}
	var meta BackupMeta
	_ = json.Unmarshal(b.Metadata, &meta)
	dir, err := m.locateBackup(ctx, b, meta)
	if err != nil {
		return View{}, err
	}
	ex, err := readExport(dir)
	if err != nil {
		return View{}, err
	}
	src := projectFromExport(ex)
	proj, err := duplicateProject(src, DuplicateRequest{Name: req.Name, Path: req.Path, Workers: true, Git: true, Start: req.Start})
	if err != nil {
		return View{}, err
	}
	m.resolveImages(&proj)
	for _, s := range proj.Services {
		if s.Image == "" && catalogKey(s) != "" && !s.Kind.IsAddon() {
			return View{}, fmt.Errorf("%w: the backup's project runs %s %s, which this Envoryx no longer offers", validate.ErrInvalid, s.Variant, s.Version)
		}
	}
	planner, err := m.planner()
	if err != nil {
		return View{}, err
	}
	if _, err := m.engine.Ping(ctx); err != nil {
		return View{}, err
	}
	unlock, err := m.lock(proj.ID)
	if err != nil {
		return View{}, err
	}
	defer unlock()

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
	if err := m.checkNewProject(ctx, proj); err != nil {
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
		m.log.Error("restoring a backup into a new project failed, rolling back", "project", proj.Slug, "backup", backupID, "step", step, "err", cause)
		rbErr := m.rollback(ctx, j)
		if delErr := m.store.Projects.Delete(context.WithoutCancel(ctx), proj.ID); delErr != nil {
			rbErr = errors.Join(rbErr, delErr)
		}
		m.audit.Log(ctx, audit.ActionProjectFailed, "project", proj.ID, map[string]any{"name": proj.Name, "restoredFrom": backupID, "step": step, "error": cause.Error()})
		m.notify(ctx, notify.Event{Kind: "project.failed", Level: notify.Error, Project: proj.Name, Title: fmt.Sprintf("Restoring %s failed", proj.Name), Message: fmt.Sprintf("Step %q failed: %v. The new project was rolled back.", step, cause)})
		if rbErr != nil {
			return View{}, fmt.Errorf("%s: %w (rollback incomplete: %v)", step, cause, rbErr)
		}
		return View{}, fmt.Errorf("%s: %w", step, cause)
	}

	for i := range proj.Workers {
		w := &proj.Workers[i]
		w.ProjectID = proj.ID
		if err := m.store.Workers.Add(ctx, w); err != nil {
			return fail("restore worker "+w.Name, err)
		}
	}
	for i, c := range ex.CronJobs {
		job := store.CronJob{ProjectID: proj.ID, Name: c.Name, Runtime: c.Runtime, Schedule: c.Schedule, Command: c.Command, Timeout: c.Timeout, Enabled: c.Enabled, Position: i}
		if err := m.store.CronJobs.Add(ctx, &job); err != nil {
			return fail("restore cron job "+c.Name, err)
		}
	}

	files := req.Files && meta.Files != nil
	target := planner.ProjectDir(proj)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		// Only a directory this operation created is removed again when it fails.
		j.projectDir = target
	} else if err == nil && files {
		entries, rerr := os.ReadDir(target)
		if rerr != nil {
			return fail("prepare project directory", rerr)
		}
		if len(entries) > 0 {
			return fail("prepare project directory", fmt.Errorf("%w: %s already exists and is not empty", ErrConflict, proj.Path))
		}
	}
	step(ctx, "Preparing the project directory")
	if err := m.ensureProjectDir(planner, proj, !files, files); err != nil {
		return fail("prepare project directory", err)
	}
	if files {
		step(ctx, "Restoring the project files")
		paths, err := m.paths()
		if err != nil {
			return fail("restore the project files", err)
		}
		if err := extractArchive(filepath.Join(dir, backupFilesFile), target, paths.PUID, paths.PGID); err != nil {
			return fail("restore the project files", err)
		}
	}
	if buildsDockerfile(proj) {
		// The image of a Dockerfile follows the restored files.
		m.resolveImages(&proj)
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return fail("plan the project", err)
	}
	j.configDir = plan.ConfigDir
	step(ctx, "Writing the configuration")
	if err := writePlanFiles(plan); err != nil {
		return fail("write configuration", err)
	}
	if failed, err := m.provision(ctx, plan, &j); err != nil {
		return fail(failed, err)
	}

	restored := map[string]any{}
	if req.Database {
		if meta.HasAnyDatabase() && len(proj.Databases()) > 0 {
			dbs, skipped, err := m.restoreDatabases(ctx, proj, meta, dir, nil)
			if err != nil {
				return fail("restore the database", err)
			}
			restored["database"] = true
			if len(dbs) > 0 {
				restored["databases"] = dbs
			}
			if len(skipped) > 0 {
				restored["skipped"] = skipped
			}
		}
		if len(meta.AddonVolumes) > 0 {
			vols, err := m.restoreAddonVolumes(ctx, proj, dir, meta.AddonVolumes)
			if err != nil {
				return fail("restore the addon volumes", err)
			}
			restored["addonVolumes"] = vols
		}
	}
	if req.Storage && meta.Storage != nil {
		if _, cfg, err := storageConfig(proj); err == nil {
			step(ctx, "Restoring the object storage bucket")
			err := m.withServiceRunning(ctx, proj, store.ServiceStorage, func(ctx context.Context) error {
				if err := m.provisionBucket(ctx, proj, cfg); err != nil {
					return err
				}
				return m.restoreStorage(ctx, proj, filepath.Join(dir, backupStorageFile), false)
			})
			if err != nil {
				return fail("restore the object storage", err)
			}
			restored["storage"] = true
		}
	}
	if req.Start {
		if failed, err := m.startProvisioned(ctx, proj, plan, j); err != nil {
			return fail(failed, err)
		}
	}
	step(ctx, "Finishing up")
	if err := m.store.Projects.UpdateState(ctx, proj.ID, proj.DesiredState, store.LifecycleReady, ""); err != nil {
		return fail("finalise the project", err)
	}
	details := map[string]any{
		"name": proj.Name, "slug": proj.Slug, "path": proj.Path, "port": proj.HTTPPort,
		"backup": backupID, "sourceProject": meta.ProjectName, "sourceId": projectID, "files": files,
	}
	for k, v := range restored {
		details[k] = v
	}
	m.audit.Log(ctx, audit.ActionBackupRestoredNew, "project", proj.ID, details)
	return m.Get(ctx, proj.ID)
}

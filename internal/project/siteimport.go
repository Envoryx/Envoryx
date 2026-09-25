package project

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/siteimport"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ImportRequest creates the project from an uploaded website (see BeginSiteImport).
type ImportRequest struct {
	// ID is the upload.
	ID string
	// AdaptConfig wires the site's configuration (wp-config.php, settings.php…) to the
	// project database; otherwise the files stay exactly as uploaded.
	AdaptConfig bool

	result *ImportResult
}

// ImportResult tells what the import did beyond creating the project.
type ImportResult struct {
	Framework siteimport.Framework `json:"framework"`
	Database  bool                 `json:"database"` // the dump was imported
	Adapted   siteimport.Adapted   `json:"adapted"`
	Notices   []siteimport.Notice  `json:"notices"`
}

// siteStaging is where uploads wait: next to the backups, which is where Envoryx keeps
// large files, in a folder no project identifier can name.
func (m *Manager) siteStaging() (siteimport.Staging, error) {
	p, err := m.paths()
	if err != nil {
		return siteimport.Staging{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return siteimport.Staging{Dir: filepath.Join(p.BackupsRoot(), ".site-imports")}, nil
}

// SiteImportOptions returns the PHP versions a site can be suggested.
func (m *Manager) SiteImportOptions() siteimport.Options {
	var opt siteimport.Options
	if r, ok := m.catalog.Get("php"); ok {
		for _, v := range r.Versions {
			if v.Preview {
				continue
			}
			opt.PHPVersions = append(opt.PHPVersions, v.Version)
			if v.Default {
				opt.DefaultPHP = v.Version
			}
		}
	}
	if opt.DefaultPHP == "" && len(opt.PHPVersions) > 0 {
		opt.DefaultPHP = opt.PHPVersions[0]
	}
	return opt
}

// BeginSiteImport starts an upload of a website.
func (m *Manager) BeginSiteImport() (*siteimport.Upload, error) {
	st, err := m.siteStaging()
	if err != nil {
		return nil, err
	}
	return st.Begin()
}

// SiteImport returns an upload that is waiting for its project.
func (m *Manager) SiteImport(id string) (siteimport.Staged, error) {
	st, err := m.siteStaging()
	if err != nil {
		return siteimport.Staged{}, err
	}
	return st.Get(id)
}

// DiscardSiteImport removes an upload.
func (m *Manager) DiscardSiteImport(id string) error {
	st, err := m.siteStaging()
	if err != nil {
		return err
	}
	return st.Remove(id)
}

// CreateFromImport creates a project from an uploaded website: the files are unpacked
// into the new project directory, the configuration is adapted if asked to, and the
// dump is imported into the project database.
func (m *Manager) CreateFromImport(ctx context.Context, req CreateRequest) (View, ImportResult, error) {
	if req.Import == nil {
		return View{}, ImportResult{}, fmt.Errorf("%w: no website upload given", validate.ErrInvalid)
	}
	var res ImportResult
	req.Import.result = &res
	view, err := m.Create(ctx, req)
	return view, res, err
}

// checkImport validates an import request before anything is created.
func (m *Manager) checkImport(req CreateRequest) (siteimport.Staged, error) {
	if req.Template != "" || req.Git != nil && req.Git.URL != "" {
		return siteimport.Staged{}, fmt.Errorf("%w: choose either an uploaded website, a template or a repository", validate.ErrInvalid)
	}
	staged, err := m.SiteImport(req.Import.ID)
	if err != nil {
		return staged, err
	}
	if staged.DumpFile() != "" {
		if req.Database == nil {
			return staged, fmt.Errorf("%w: the uploaded database dump needs a database; choose one", validate.ErrInvalid)
		}
		t := req.Database.Type
		if t == "mongodb" {
			return staged, fmt.Errorf("%w: an SQL dump cannot be imported into MongoDB", validate.ErrInvalid)
		}
		if v := staged.Analysis.Dump.Variant; v == "postgresql" && t != "postgresql" || v != "" && v != "postgresql" && t == "postgresql" {
			return staged, fmt.Errorf("%w: the dump comes from %s and cannot be imported into %s", validate.ErrInvalid, v, t)
		}
	}
	return staged, nil
}

// unpackSite fills the (new, empty) project directory from the upload and adapts the
// configuration.
func (m *Manager) unpackSite(ctx context.Context, planner *Planner, proj store.Project, staged siteimport.Staged, imp *ImportRequest) error {
	dir := planner.ProjectDir(proj)
	step(ctx, "Unpacking {{file}}", "file", staged.SiteName)
	if err := siteimport.Extract(staged.SiteFile(), dir, staged.Analysis.Root, planner.paths.PUID, planner.paths.PGID); err != nil {
		return err
	}
	if !imp.AdaptConfig {
		return nil
	}
	db := siteimport.Database{Host: "database"}
	if svc, cfg, err := databaseConfig(proj); err == nil {
		db.Variant, db.Name, db.User, db.Password = svc.Variant, cfg.Database, cfg.Username, cfg.Password
		if d, err := dialectOf(svc); err == nil {
			db.Port = d.Port
		}
	} else if !errors.Is(err, ErrNoDatabase) {
		return err
	}
	step(ctx, "Adapting the configuration of the website")
	adapted, err := siteimport.Adapt(dir, staged.Analysis, db, planner.paths.PUID, planner.paths.PGID)
	if imp.result != nil {
		imp.result.Adapted = adapted
	}
	return err
}

// importDump reads the uploaded dump into the project database, starting the database
// container for it when the project is not started.
func (m *Manager) importDump(ctx context.Context, proj store.Project, file string) error {
	svc, cfg, err := databaseConfig(proj)
	if err != nil {
		return err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	if dialect.DumpFormat == "archive" {
		return fmt.Errorf("%w: an SQL dump cannot be imported into %s", validate.ErrInvalid, svc.Variant)
	}
	return m.withServiceRunning(ctx, proj, store.ServiceDatabase, func(ctx context.Context) error {
		if err := m.waitForDatabase(ctx, proj, svc, cfg, dialect); err != nil {
			return err
		}
		c, err := m.ServiceContainer(ctx, proj.ID, store.ServiceDatabase)
		if err != nil {
			return err
		}
		step(ctx, "Importing the database dump")
		r, err := siteimport.OpenDumpForImport(file, svc.Variant)
		if err != nil {
			return err
		}
		defer r.Close()
		var stderr strings.Builder
		argv, env := dialect.Restore(cfg)
		code, err := m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{Cmd: argv, Env: env, Stdin: r, Stderr: &limitedBuilder{b: &stderr}})
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("%w: the dump could not be imported (exit %d): %s", validate.ErrInvalid, code, sanitizeSQLError(strings.TrimSpace(stderr.String()), cfg))
		}
		return nil
	})
}

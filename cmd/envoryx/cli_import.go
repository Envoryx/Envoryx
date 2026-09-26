// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const importUsage = `Usage: envoryx import <folder|archive> [name] [flags]

Creates a project from an existing website: a folder on this machine (packed and
uploaded as it is) or a ZIP/tar.gz archive, optionally with a database dump. Envoryx
recognises WordPress, Laravel, Symfony, Drupal, TYPO3, Joomla and plain PHP or HTML
sites and picks PHP version, extensions, document root, web server and database;
the flags below override what it suggests. The name defaults to the folder's.

  --db FILE            SQL dump (.sql or .sql.gz) to import into the project database
  --no-adapt           leave the site's configuration files exactly as uploaded
                       (otherwise wp-config.php & co. are wired to the project database)
  --path DIR           project directory below the projects folder
  --php VERSION        PHP version
  --database TYPE[:V]  mariadb, mysql or postgres; "none" for no database
  --web SERVER         apache, nginx or caddy
  --docroot DIR        document root
  --start              start the project once it is created
  --dry-run            only upload and show what Envoryx recognised
`

// siteAnalysis is the part of the server's analysis the CLI shows and builds on.
type siteAnalysis struct {
	Root      string `json:"root"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Framework struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"framework"`
	Runtime    string `json:"runtime"`
	PHPVersion string `json:"phpVersion"`
	Docroot    string `json:"docroot"`
	Web        string `json:"web"`
	Database   string `json:"database"`
	Config     *struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	} `json:"config"`
	Notices []struct {
		Level  string            `json:"level"`
		Text   string            `json:"text"`
		Params map[string]string `json:"params"`
	} `json:"notices"`
}

type siteImport struct {
	ID       string       `json:"id"`
	SiteName string       `json:"siteName"`
	DumpName string       `json:"dumpName"`
	Analysis siteAnalysis `json:"analysis"`
}

type importSpec struct {
	ID          string `json:"id"`
	AdaptConfig bool   `json:"adaptConfig"`
}

var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

func (c *cli) importSite(ctx context.Context, args []string) error {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(c.errOut, importUsage)
		return nil
	}
	fs := c.newFlags("import")
	var (
		dump     = fs.String("db", "", "SQL dump")
		noAdapt  = fs.Bool("no-adapt", false, "leave the configuration alone")
		path     = fs.String("path", "", "project directory")
		php      = fs.String("php", "", "PHP version")
		database = fs.String("database", "", "database type[:version]")
		web      = fs.String("web", "", "web server")
		docroot  = fs.String("docroot", "", "document root")
		start    = fs.Bool("start", false, "start the project")
		dryRun   = fs.Bool("dry-run", false, "only analyse")
	)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	source := arg(pos, 0)
	if source == "" {
		fmt.Fprint(c.errOut, importUsage)
		return usagef("which folder or archive should be imported?")
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	name := arg(pos, 1)
	if name == "" {
		abs, _ := filepath.Abs(source)
		name = strings.TrimSpace(regexp.MustCompile(`(?i)\.(zip|tgz|tar\.gz|tar)$`).ReplaceAllString(filepath.Base(abs), ""))
	}
	if *dump != "" {
		if _, err := os.Stat(*dump); err != nil {
			return err
		}
	}

	api, err := c.connect()
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()

	if info.IsDir() {
		fmt.Fprintf(c.errOut, "Packing and uploading %s…\n", source)
	} else {
		fmt.Fprintf(c.errOut, "Uploading %s (%s)…\n", source, humanSize(info.Size()))
	}
	staged, err := uploadSite(ctx, api, source, info.IsDir(), *dump)
	if err != nil {
		return err
	}
	a := staged.Analysis
	discard := func() {
		_ = api.do(context.WithoutCancel(ctx), http.MethodDelete, "/api/v1/site-imports/"+staged.ID, nil, nil, nil)
	}

	req := createRequest{Name: name, Path: *path, Docroot: a.Docroot, Start: *start, Import: &importSpec{ID: staged.ID, AdaptConfig: !*noAdapt}}
	switch a.Runtime {
	case "php":
		req.PHP = &phpSpec{Version: a.PHPVersion}
	case "node":
		req.Node = &nodeSpec{}
	case "python":
		req.Python = &pythonSpec{}
	case "go":
		req.Go = &goSpec{}
	case "ruby":
		req.Ruby = &rubySpec{}
	}
	if *php != "" {
		req.PHP = &phpSpec{Version: runtimeVersion(*php)}
	}
	if *docroot != "" {
		req.Docroot = *docroot
	}
	webServer := a.Web
	if *web != "" {
		webServer = *web
	}
	if webServer != "" {
		req.Web, _ = json.Marshal(map[string]string{"type": webServer})
	}
	if a.Database != "" {
		req.Database = &databaseSpec{Type: a.Database}
	}
	if *database != "" {
		if *database == "none" {
			req.Database = nil
		} else {
			kind, version, _ := strings.Cut(*database, ":")
			req.Database = &databaseSpec{Type: databaseType(kind), Version: runtimeVersion(version)}
		}
	}

	if c.json && *dryRun {
		discard()
		return c.printJSON(staged)
	}
	if !c.json {
		c.printAnalysis(staged, req)
	}
	if *dryRun {
		discard()
		c.printf("Dry run: nothing was created.\n")
		return nil
	}

	var body struct {
		Project projectSummary `json:"project"`
		Import  struct {
			Database bool `json:"database"`
			Adapted  struct {
				Changed   []string `json:"changed"`
				Originals []string `json:"originals"`
				Removed   []string `json:"removed"`
			} `json:"adapted"`
		} `json:"import"`
	}
	if err := api.post(ctx, "/api/v1/projects", req, &body); err != nil {
		discard()
		return err
	}
	if c.json {
		return c.printJSON(body)
	}
	c.printf("Created %s (%s)\n", body.Project.Name, body.Project.Slug)
	for _, f := range body.Import.Adapted.Changed {
		c.printf("  adapted %s\n", f)
	}
	for _, f := range body.Import.Adapted.Originals {
		c.printf("  original kept as %s\n", f)
	}
	for _, f := range body.Import.Adapted.Removed {
		c.printf("  removed %s\n", f)
	}
	if body.Import.Database {
		c.printf("  imported %s into the project database\n", staged.DumpName)
	}
	if u := body.Project.URL(); u != "" {
		c.printf("  %s\n", u)
	}
	if !req.Start {
		c.printf("  Start it with: envoryx project start %s\n", body.Project.Slug)
	}
	return nil
}

func (c *cli) printAnalysis(s siteImport, req createRequest) {
	a := s.Analysis
	what := strings.TrimSpace(a.Framework.Name + " " + a.Framework.Version)
	c.printf("Recognised %s (%d files, %s)\n", what, a.Files, humanSize(a.Bytes))
	var parts []string
	if req.PHP != nil {
		parts = append(parts, "PHP "+orDefault(req.PHP.Version))
	} else if a.Runtime != "" {
		parts = append(parts, a.Runtime)
	}
	docroot := req.Docroot
	if docroot == "" {
		docroot = "(project root)"
	}
	parts = append(parts, "document root "+docroot)
	if len(req.Web) > 0 {
		var w struct{ Type string }
		_ = json.Unmarshal(req.Web, &w)
		parts = append(parts, w.Type)
	}
	if req.Database != nil {
		parts = append(parts, req.Database.Type)
	} else {
		parts = append(parts, "no database")
	}
	c.printf("  %s\n", strings.Join(parts, " · "))
	if a.Config != nil {
		switch {
		case a.Config.Mode == "adapt" && req.Import.AdaptConfig:
			c.printf("  %s is wired to the project database\n", a.Config.Path)
		case a.Config.Mode == "env":
			c.printf("  the injected DB_* variables override %s\n", a.Config.Path)
		default:
			c.printf("  change the database connection in %s (host \"database\", see the Database tab)\n", a.Config.Path)
		}
	}
	for _, n := range a.Notices {
		text := placeholderRe.ReplaceAllStringFunc(n.Text, func(m string) string {
			return n.Params[placeholderRe.FindStringSubmatch(m)[1]]
		})
		prefix := "  note: "
		if n.Level == "warning" {
			prefix = "  warning: "
		}
		c.printf("%s%s\n", prefix, text)
	}
}

func orDefault(v string) string {
	if v == "" {
		return "(default)"
	}
	return v
}

// uploadSite streams the site (a folder packed on the fly, or an archive as it is) and
// the dump to the server as one multipart request; nothing is written to disk here.
func uploadSite(ctx context.Context, api *client, source string, isDir bool, dump string) (siteImport, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			siteName := filepath.Base(source)
			if isDir {
				siteName += ".tar.gz"
			}
			part, err := mw.CreateFormFile("site", siteName)
			if err != nil {
				return err
			}
			if isDir {
				err = packDir(source, part)
			} else {
				err = copyFile(source, part)
			}
			if err != nil {
				return err
			}
			if dump != "" {
				part, err := mw.CreateFormFile("database", filepath.Base(dump))
				if err != nil {
					return err
				}
				if err := copyFile(dump, part); err != nil {
					return err
				}
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()
	var out struct {
		Import siteImport `json:"import"`
	}
	err := api.upload(ctx, "/api/v1/site-imports", mw.FormDataContentType(), pr, &out)
	_ = pr.Close()
	return out.Import, err
}

func copyFile(name string, w io.Writer) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// packDir writes the folder as a gzipped tarball: files, folders and symlinks with their
// modes; sockets and devices are left out.
func packDir(dir string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			return copyFile(p, tw)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

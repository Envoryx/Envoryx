// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type backupInfo struct {
	ID        string    `json:"id"`
	Dir       string    `json:"dir"`
	Kind      string    `json:"kind"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
	Missing   bool      `json:"missing"`
	Meta      struct {
		Note     string `json:"note"`
		Source   string `json:"source"`
		Database *struct {
			Name string `json:"name"`
		} `json:"database"`
		Databases []struct {
			DB string `json:"db"`
		} `json:"databases"`
		Files *struct {
			Entries int `json:"entries"`
		} `json:"files"`
		Storage *struct {
			Objects int `json:"objects"`
		} `json:"storage"`
	} `json:"meta"`
}

// contents names what is inside a backup, which is what a reader looks for first.
func (b backupInfo) contents() string {
	var parts []string
	switch n := len(b.Meta.Databases); {
	case n > 0 && b.Meta.Database != nil:
		parts = append(parts, fmt.Sprintf("%d databases", n+1))
	case n > 0:
		for _, d := range b.Meta.Databases {
			parts = append(parts, "database "+d.DB)
		}
	case b.Meta.Database != nil:
		parts = append(parts, "database")
	}
	if b.Meta.Files != nil {
		parts = append(parts, "files")
	}
	if b.Meta.Storage != nil {
		parts = append(parts, "storage")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "+")
}

func (c *cli) backupCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help", "-help":
		return groupHelp("backup")
	case "", "list", "ls":
		return c.backupList(ctx, rest)
	case "create", "new":
		return c.backupCreate(ctx, rest)
	case "restore":
		return c.backupRestore(ctx, rest)
	case "download":
		return c.backupDownload(ctx, rest)
	case "delete", "rm":
		return c.backupDelete(ctx, rest)
	case "offsite", "upload":
		return c.backupOffsite(ctx, rest)
	case "remote":
		return c.backupRemote(ctx, rest)
	case "fetch":
		return c.backupFetch(ctx, rest)
	case "recover":
		return c.backupRecover(ctx, rest)
	default:
		return usagef("unknown backup command %q", cmd)
	}
}

func (c *cli) backupList(ctx context.Context, args []string) error {
	fs := c.newFlags("backup list")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	if c.json {
		return c.printRaw(ctx, api, projectPath(p.ID, "backups"), nil, "backups")
	}
	var body backupList
	if err := api.get(ctx, projectPath(p.ID, "backups"), nil, &body); err != nil {
		return err
	}
	if len(body.Backups) == 0 {
		c.printf("No backups of %s yet.\n", p.Slug)
		return nil
	}
	rows := make([][]string, 0, len(body.Backups))
	for _, b := range body.Backups {
		note := b.Meta.Note
		if b.Missing {
			note = "archive missing - " + note
		}
		source := b.Meta.Source
		if source == "" {
			source = "manual" // as the offsite listing calls it
		}
		row := []string{b.ID, b.CreatedAt.Local().Format("2006-01-02 15:04"), b.contents(), humanSize(b.SizeBytes), source}
		if len(body.OffsiteTargets) > 0 {
			row = append(row, offsiteSummary(body.Offsite[b.ID]))
		}
		rows = append(rows, append(row, note))
	}
	header := []string{"ID", "CREATED", "CONTENTS", "SIZE", "SOURCE"}
	if len(body.OffsiteTargets) > 0 {
		header = append(header, "OFFSITE")
	}
	c.table(append(header, "NOTE"), rows)
	return nil
}

func (c *cli) backupCreate(ctx context.Context, args []string) error {
	fs := c.newFlags("backup create")
	note := fs.String("note", "", "note to store with the backup")
	noDatabase := fs.Bool("no-database", false, "leave the database out")
	noFiles := fs.Bool("no-files", false, "leave the project files out")
	noStorage := fs.Bool("no-storage", false, "leave the object storage out")
	deps := fs.Bool("dependencies", false, "keep vendor/ and node_modules/ in the archive")
	offsite := fs.Bool("offsite", false, "copy it to every enabled offsite target")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	// Everything the project has, unless something is explicitly left out. Storage is
	// ignored by the server when the project has no bucket.
	body := map[string]any{
		"database":            !*noDatabase,
		"files":               !*noFiles,
		"storage":             !*noStorage,
		"includeDependencies": *deps,
		"note":                *note,
	}
	if *offsite {
		body["offsite"] = true
	}
	var out struct {
		Backup       backupInfo    `json:"backup"`
		Offsite      []offsiteCopy `json:"offsite"`
		OffsiteError string        `json:"offsiteError"`
	}
	if err := api.post(ctx, projectPath(p.ID, "backups"), body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Backup %s of %s: %s, %s\n", out.Backup.ID, p.Slug, out.Backup.contents(), humanSize(out.Backup.SizeBytes))
	if out.OffsiteError != "" {
		return fmt.Errorf("the backup was created, but not copied offsite: %s", out.OffsiteError)
	}
	for _, o := range out.Offsite {
		c.printf("  Copying to %s in the background (envoryx backup list %s shows how it went)\n", o.TargetName, p.Slug)
	}
	return nil
}

func (c *cli) backupRestore(ctx context.Context, args []string) error {
	fs := c.newFlags("backup restore")
	yes := fs.Bool("yes", false, "confirm")
	noDatabase := fs.Bool("no-database", false, "do not restore the database")
	noFiles := fs.Bool("no-files", false, "do not restore the files")
	noStorage := fs.Bool("no-storage", false, "do not restore the object storage")
	wipeFiles := fs.Bool("wipe-files", false, "empty the project directory first")
	wipeStorage := fs.Bool("wipe-storage", false, "empty the bucket first")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	backup := arg(pos, 1)
	if backup == "" {
		return usagef("which backup? envoryx backup list %s shows them", p.Slug)
	}
	if !*yes {
		return usagef("restoring overwrites the current state of %s; confirm with --yes", p.Slug)
	}
	// Restore what this backup actually holds: asking for a part it does not contain is
	// refused, and nobody wants to spell out the contents of an archive they just listed.
	has, err := c.backupContents(ctx, api, p, backup)
	if err != nil {
		return err
	}
	body := map[string]any{
		"database":    has.database && !*noDatabase,
		"files":       has.files && !*noFiles,
		"storage":     has.storage && !*noStorage,
		"wipeFiles":   *wipeFiles,
		"wipeStorage": *wipeStorage,
		"confirm":     p.Slug,
	}
	var out struct {
		Backup backupInfo `json:"backup"`
	}
	if err := api.post(ctx, projectPath(p.ID, "backups", backup, "restore"), body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Restored %s into %s.\n", backup, p.Slug)
	return nil
}

func (c *cli) backupDownload(ctx context.Context, args []string) error {
	fs := c.newFlags("backup download")
	out := fs.String("o", "", "file to write (default: the archive's name, not over an existing file; \"-\" is stdout)")
	fs.StringVar(out, "output", "", "file to write")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	backup := arg(pos, 1)
	if backup == "" {
		return usagef("which backup? envoryx backup list %s shows them", p.Slug)
	}
	resp, err := api.request(ctx, http.MethodGet, projectPath(p.ID, "backups", backup, "download"), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return c.saveDownload(resp, *out, p.Slug+"-"+backup+".tar", 0o600)
}

func (c *cli) backupDelete(ctx context.Context, args []string) error {
	fs := c.newFlags("backup delete")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	backup := arg(pos, 1)
	if backup == "" {
		return usagef("which backup? envoryx backup list %s shows them", p.Slug)
	}
	if !*yes {
		return usagef("deleting backup %s cannot be undone; confirm with --yes", backup)
	}
	if err := api.do(ctx, http.MethodDelete, projectPath(p.ID, "backups", backup), nil, nil, nil); err != nil {
		return err
	}
	c.printf("Deleted backup %s.\n", backup)
	return nil
}

type backupParts struct{ database, files, storage bool }

// backupContents asks what is inside one backup. An archive the server does not list is
// left to the server to reject, with database and files assumed.
func (c *cli) backupContents(ctx context.Context, api *client, p projectSummary, id string) (backupParts, error) {
	var body struct {
		Backups []backupInfo `json:"backups"`
	}
	if err := api.get(ctx, projectPath(p.ID, "backups"), nil, &body); err != nil {
		return backupParts{}, err
	}
	for _, b := range body.Backups {
		if b.ID == id {
			return backupParts{database: b.Meta.Database != nil, files: b.Meta.Files != nil, storage: b.Meta.Storage != nil}, nil
		}
	}
	return backupParts{database: true, files: true}, nil
}

// projectAndClient is the opening move of every project-scoped command.
func (c *cli) projectAndClient(ctx context.Context, want string) (projectSummary, *client, error) {
	p, err := c.findProject(ctx, want)
	if err != nil {
		return projectSummary{}, nil, err
	}
	api, err := c.connect()
	if err != nil {
		return projectSummary{}, nil, err
	}
	return p, api, nil
}

// downloadName is what a name the server suggests must look like: a plain file name, no
// path, no dot file (.bashrc), nothing a shell or terminal treats specially.
var downloadName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)

// filenameFromResponse reads the name the server suggests for a download, or fallback
// when there is none or it isn't a plain file name.
func filenameFromResponse(disposition, fallback string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err == nil {
		if name := params["filename"]; downloadName.MatchString(name) {
			return name
		}
	}
	return fallback
}

// saveDownload writes a download to target, "-" for stdout. Without one it goes to the
// name the server suggests (filenameFromResponse) in the current directory, but never
// over a file that is there: only a target the user named is overwritten.
func (c *cli) saveDownload(resp *http.Response, target, fallback string, perm os.FileMode) error {
	if target == "-" {
		_, err := io.Copy(c.out, resp.Body)
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if target == "" {
		target = filenameFromResponse(resp.Header.Get("Content-Disposition"), fallback)
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(target, flags, perm)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s exists already; remove it or name the file to write with -o", target)
	}
	if err != nil {
		return err
	}
	written, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	c.printf("Wrote %s (%s)\n", target, humanSize(written))
	return nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

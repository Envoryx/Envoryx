// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// dbQuery addresses one database of a project: an additional one by name, the primary
// with none.
func dbQuery(db string) url.Values {
	if db == "" {
		return nil
	}
	return url.Values{"db": {db}}
}

// dbOf names the database in messages.
func dbOf(slug, db string) string {
	if db == "" {
		return "the database of " + slug
	}
	return "the database " + db + " of " + slug
}

// dbNotFound names what the API's plain "does not exist" means for a database call: a
// project without a database, an additional database it doesn't have, or a snapshot
// that isn't there.
func dbNotFound(ctx context.Context, api *client, p projectSummary, db, snapshot string, err error) error {
	if !isNotFound(err) {
		return err
	}
	if !p.hasService("database") {
		return fmt.Errorf("%s has no database", p.Slug)
	}
	if snapshot != "" && api.get(ctx, projectPath(p.ID, "database", "snapshots"), dbQuery(db), nil) == nil {
		list := "envoryx db snapshots " + p.Slug
		if db != "" {
			list += " --db " + db
		}
		return fmt.Errorf("%s has no snapshot %q (%s shows them)", dbOf(p.Slug, db), snapshot, list)
	}
	if db != "" {
		return fmt.Errorf("%s has no additional database %q", p.Slug, db)
	}
	return err
}

func (c *cli) dbCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help", "-help":
		return groupHelp("db")
	case "", "snapshots":
		return c.dbSnapshots(ctx, rest)
	case "snapshot":
		return c.dbSnapshot(ctx, rest)
	case "restore":
		return c.dbRestore(ctx, rest)
	case "clone":
		return c.dbClone(ctx, rest)
	default:
		return usagef("unknown db command %q", cmd)
	}
}

func (c *cli) dbSnapshots(ctx context.Context, args []string) error {
	fs := c.newFlags("db snapshots")
	db := fs.String("db", "", "additional database (default: the primary)")
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
		return c.printRaw(ctx, api, projectPath(p.ID, "database", "snapshots"), dbQuery(*db), "snapshots")
	}
	var body struct {
		Snapshots []backupInfo `json:"snapshots"`
	}
	if err := api.get(ctx, projectPath(p.ID, "database", "snapshots"), dbQuery(*db), &body); err != nil {
		return dbNotFound(ctx, api, p, *db, "", err)
	}
	if len(body.Snapshots) == 0 {
		c.printf("No snapshots of %s yet.\n", dbOf(p.Slug, *db))
		return nil
	}
	rows := make([][]string, 0, len(body.Snapshots))
	for _, s := range body.Snapshots {
		note := s.Meta.Note
		if s.Missing {
			note = "dump missing - " + note
		}
		rows = append(rows, []string{s.ID, s.CreatedAt.Local().Format("2006-01-02 15:04"), humanSize(s.SizeBytes), s.Meta.Source, note})
	}
	c.table([]string{"ID", "CREATED", "SIZE", "SOURCE", "NOTE"}, rows)
	return nil
}

func (c *cli) dbSnapshot(ctx context.Context, args []string) error {
	fs := c.newFlags("db snapshot")
	note := fs.String("note", "", "note to store with the snapshot")
	db := fs.String("db", "", "additional database (default: the primary)")
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
	var out struct {
		Snapshot backupInfo `json:"snapshot"`
	}
	if err := api.do(ctx, http.MethodPost, projectPath(p.ID, "database", "snapshots"), dbQuery(*db), map[string]any{"note": *note}, &out); err != nil {
		return dbNotFound(ctx, api, p, *db, "", err)
	}
	if c.json {
		return c.printJSON(out.Snapshot)
	}
	c.printf("Snapshot %s of %s (%s)\n", out.Snapshot.ID, dbOf(p.Slug, *db), humanSize(out.Snapshot.SizeBytes))
	return nil
}

func (c *cli) dbRestore(ctx context.Context, args []string) error {
	fs := c.newFlags("db restore")
	yes := fs.Bool("yes", false, "confirm")
	db := fs.String("db", "", "additional database (default: the primary)")
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
	snapshot := arg(pos, 1)
	if snapshot == "" {
		return usagef("which snapshot? envoryx db snapshots %s shows them", p.Slug)
	}
	if !*yes {
		return usagef("restoring replaces %s and everything written since; confirm with --yes", dbOf(p.Slug, *db))
	}
	if snapshot, err = c.resolveSnapshot(ctx, api, p, *db, snapshot); err != nil {
		return err
	}
	var out struct {
		Snapshot backupInfo `json:"snapshot"`
	}
	if err := api.do(ctx, http.MethodPost, projectPath(p.ID, "database", "snapshots", snapshot, "restore"), dbQuery(*db), map[string]any{"confirm": p.Slug}, &out); err != nil {
		return dbNotFound(ctx, api, p, *db, snapshot, err)
	}
	if c.json {
		return c.printJSON(out.Snapshot)
	}
	c.printf("Restored %s from snapshot %s.\n", dbOf(p.Slug, *db), snapshot)
	return nil
}

// resolveSnapshot finds the snapshot a prefix of its ID names (the first block of the
// ID is usually enough), so it needn't be typed in full. A full ID goes as it is.
func (c *cli) resolveSnapshot(ctx context.Context, api *client, p projectSummary, db, id string) (string, error) {
	if len(id) == 36 {
		return id, nil
	}
	var body struct {
		Snapshots []backupInfo `json:"snapshots"`
	}
	if err := api.get(ctx, projectPath(p.ID, "database", "snapshots"), dbQuery(db), &body); err != nil {
		return "", dbNotFound(ctx, api, p, db, id, err)
	}
	var found []string
	for _, s := range body.Snapshots {
		if strings.HasPrefix(s.ID, id) {
			found = append(found, s.ID)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no snapshot of %s starts with %q; envoryx db snapshots %s lists them", dbOf(p.Slug, db), id, p.Slug)
	case 1:
		return found[0], nil
	}
	return "", usagef("%q fits %d snapshots; give more of the ID", id, len(found))
}

func (c *cli) dbClone(ctx context.Context, args []string) error {
	fs := c.newFlags("db clone")
	from := fs.String("from", "", "project whose database is copied")
	yes := fs.Bool("yes", false, "confirm")
	noSnapshot := fs.Bool("no-snapshot", false, "do not snapshot the target's database first")
	db := fs.String("db", "", "additional database of the target (default: the primary)")
	sourceDB := fs.String("source-db", "", "database of the source (default: the one named like --db; \"primary\" for its primary)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	target, api, err := c.projectAndClient(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	// The source may also stand second, which reads like a copy: "db clone local staging".
	source := *from
	if source == "" {
		source = arg(pos, 1)
	}
	if source == "" {
		return usagef("where should the data come from? envoryx db clone %s --from staging", target.Slug)
	}
	src, err := c.findProject(ctx, source)
	if err != nil {
		return err
	}
	if !*yes {
		return usagef("cloning replaces %s with the one of %s; confirm with --yes", dbOf(target.Slug, *db), src.Slug)
	}
	var out struct {
		Clone struct {
			Source   string      `json:"source"`
			Database string      `json:"database"`
			Snapshot *backupInfo `json:"snapshot"`
		} `json:"clone"`
	}
	body := map[string]any{"source": src.ID, "snapshot": !*noSnapshot, "confirm": target.Slug}
	switch *sourceDB {
	case "":
	case "primary":
		body["sourceDb"] = ""
	default:
		body["sourceDb"] = *sourceDB
	}
	if err := api.do(ctx, http.MethodPost, projectPath(target.ID, "database", "clone"), dbQuery(*db), body, &out); err != nil {
		if !src.hasService("database") {
			return dbNotFound(ctx, api, src, "", "", err)
		}
		return dbNotFound(ctx, api, target, *db, "", err)
	}
	if c.json {
		return c.printJSON(out.Clone)
	}
	// The source database is the one named like the target's unless --source-db says.
	srcDB := *db
	switch *sourceDB {
	case "":
	case "primary":
		srcDB = ""
	default:
		srcDB = *sourceDB
	}
	c.printf("Copied %s into %s.\n", dbOf(src.Slug, srcDB), dbOf(target.Slug, *db))
	if out.Clone.Snapshot != nil {
		c.printf("The state before is snapshot %s - envoryx db restore %s %s --yes puts it back.\n", out.Clone.Snapshot.ID, target.Slug, out.Clone.Snapshot.ID)
	}
	return nil
}

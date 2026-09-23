// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
)

func (c *cli) dbCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIUsage
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
		return c.printRaw(ctx, api, projectPath(p.ID, "database", "snapshots"), nil, "snapshots")
	}
	var body struct {
		Snapshots []backupInfo `json:"snapshots"`
	}
	if err := api.get(ctx, projectPath(p.ID, "database", "snapshots"), nil, &body); err != nil {
		return err
	}
	if len(body.Snapshots) == 0 {
		c.printf("No database snapshots of %s yet.\n", p.Slug)
		return nil
	}
	rows := make([][]string, 0, len(body.Snapshots))
	for _, s := range body.Snapshots {
		note := s.Meta.Note
		if s.Missing {
			note = "dump missing – " + note
		}
		rows = append(rows, []string{s.ID, s.CreatedAt.Local().Format("2006-01-02 15:04"), humanSize(s.SizeBytes), s.Meta.Source, note})
	}
	c.table([]string{"ID", "CREATED", "SIZE", "SOURCE", "NOTE"}, rows)
	return nil
}

func (c *cli) dbSnapshot(ctx context.Context, args []string) error {
	fs := c.newFlags("db snapshot")
	note := fs.String("note", "", "note to store with the snapshot")
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
	if err := api.post(ctx, projectPath(p.ID, "database", "snapshots"), map[string]any{"note": *note}, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Snapshot)
	}
	c.printf("Snapshot %s of %s (%s)\n", out.Snapshot.ID, p.Slug, humanSize(out.Snapshot.SizeBytes))
	return nil
}

func (c *cli) dbRestore(ctx context.Context, args []string) error {
	fs := c.newFlags("db restore")
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
	snapshot := arg(pos, 1)
	if snapshot == "" {
		return usagef("which snapshot? envoryx db snapshots %s shows them", p.Slug)
	}
	if !*yes {
		return fmt.Errorf("restoring replaces the database of %s and everything written since; confirm with --yes", p.Slug)
	}
	var out struct {
		Snapshot backupInfo `json:"snapshot"`
	}
	if err := api.post(ctx, projectPath(p.ID, "database", "snapshots", snapshot, "restore"), map[string]any{"confirm": p.Slug}, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Snapshot)
	}
	c.printf("Restored the database of %s from snapshot %s.\n", p.Slug, snapshot)
	return nil
}

func (c *cli) dbClone(ctx context.Context, args []string) error {
	fs := c.newFlags("db clone")
	from := fs.String("from", "", "project whose database is copied")
	yes := fs.Bool("yes", false, "confirm")
	noSnapshot := fs.Bool("no-snapshot", false, "do not snapshot the target's database first")
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
		return fmt.Errorf("cloning replaces the database of %s with the one of %s; confirm with --yes", target.Slug, src.Slug)
	}
	var out struct {
		Clone struct {
			Source   string      `json:"source"`
			Database string      `json:"database"`
			Snapshot *backupInfo `json:"snapshot"`
		} `json:"clone"`
	}
	body := map[string]any{"source": src.ID, "snapshot": !*noSnapshot, "confirm": target.Slug}
	if err := api.post(ctx, projectPath(target.ID, "database", "clone"), body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Clone)
	}
	c.printf("Copied the database of %s into %s (%s).\n", src.Slug, target.Slug, out.Clone.Database)
	if out.Clone.Snapshot != nil {
		c.printf("The state before is snapshot %s – envoryx db restore %s %s --yes puts it back.\n", out.Clone.Snapshot.ID, target.Slug, out.Clone.Snapshot.ID)
	}
	return nil
}

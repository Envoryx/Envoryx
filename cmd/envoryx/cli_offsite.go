// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Offsite copies of project backups: envoryx backup offsite|remote|fetch. The targets
// themselves are set up in the web interface (Settings → Backups).

type offsiteCopy struct {
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	Status     string `json:"status"`
	Error      string `json:"error"`
}

type offsiteTarget struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type backupList struct {
	Backups        []backupInfo             `json:"backups"`
	Offsite        map[string][]offsiteCopy `json:"offsite"`
	OffsiteTargets []offsiteTarget          `json:"offsiteTargets"`
}

type remoteBackup struct {
	Key       string    `json:"key"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	SizeBytes int64     `json:"sizeBytes"`
	Encrypted bool      `json:"encrypted"`
}

// offsiteSummary is the OFFSITE column: "B2 ✓, box failed".
func offsiteSummary(copies []offsiteCopy) string {
	if len(copies) == 0 {
		return "–"
	}
	parts := make([]string, 0, len(copies))
	for _, o := range copies {
		state := map[string]string{"done": "✓", "failed": "failed", "running": "uploading", "pending": "queued"}[o.Status]
		parts = append(parts, o.TargetName+" "+state)
	}
	return strings.Join(parts, ", ")
}

// offsiteTargetFor picks the target a command means: the named one, or the only enabled
// one.
func offsiteTargetFor(targets []offsiteTarget, want string) (offsiteTarget, error) {
	var enabled []offsiteTarget
	for _, t := range targets {
		if want != "" && (t.ID == want || strings.EqualFold(t.Name, want)) {
			return t, nil
		}
		if t.Enabled {
			enabled = append(enabled, t)
		}
	}
	if want != "" {
		return offsiteTarget{}, fmt.Errorf("no offsite target %q", want)
	}
	switch len(enabled) {
	case 0:
		return offsiteTarget{}, fmt.Errorf("no offsite target is set up – add one in the web interface under Settings → Backups")
	case 1:
		return enabled[0], nil
	}
	names := make([]string, len(enabled))
	for i, t := range enabled {
		names[i] = t.Name
	}
	return offsiteTarget{}, usagef("several offsite targets (%s) – pick one with --target", strings.Join(names, ", "))
}

func (c *cli) backupListing(ctx context.Context, want string) (projectSummary, *client, backupList, error) {
	p, api, err := c.projectAndClient(ctx, want)
	if err != nil {
		return projectSummary{}, nil, backupList{}, err
	}
	var list backupList
	if err := api.get(ctx, projectPath(p.ID, "backups"), nil, &list); err != nil {
		return projectSummary{}, nil, backupList{}, err
	}
	return p, api, list, nil
}

// backupOffsite copies a backup to the offsite targets (all enabled ones, or --target).
func (c *cli) backupOffsite(ctx context.Context, args []string) error {
	fs := c.newFlags("backup offsite")
	target := fs.String("target", "", "offsite target name or id (default: every enabled one)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, list, err := c.backupListing(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	backup := arg(pos, 1)
	if backup == "" {
		return usagef("which backup? envoryx backup list %s shows them", p.Slug)
	}
	body := map[string]any{"targets": []string{}}
	if *target != "" {
		t, err := offsiteTargetFor(list.OffsiteTargets, *target)
		if err != nil {
			return err
		}
		body["targets"] = []string{t.ID}
	}
	var out struct {
		Offsite []offsiteCopy `json:"offsite"`
	}
	if err := api.post(ctx, projectPath(p.ID, "backups", backup, "offsite"), body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Offsite)
	}
	for _, o := range out.Offsite {
		c.printf("Copying %s to %s in the background\n", backup, o.TargetName)
	}
	return nil
}

// backupRemote lists the project's backups on an offsite target.
func (c *cli) backupRemote(ctx context.Context, args []string) error {
	fs := c.newFlags("backup remote")
	target := fs.String("target", "", "offsite target name or id")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, list, err := c.backupListing(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	t, err := offsiteTargetFor(list.OffsiteTargets, *target)
	if err != nil {
		return err
	}
	var out struct {
		Backups []remoteBackup `json:"backups"`
	}
	if err := api.get(ctx, projectPath(p.ID, "offsite", t.ID), nil, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backups)
	}
	if len(out.Backups) == 0 {
		c.printf("No backups of %s on %s.\n", p.Slug, t.Name)
		return nil
	}
	local := map[string]bool{}
	for _, b := range list.Backups {
		if !b.Missing {
			local[b.Dir] = true
		}
	}
	rows := make([][]string, 0, len(out.Backups))
	for _, b := range out.Backups {
		here := ""
		if local[b.ID] {
			here = "also here"
		}
		enc := ""
		if b.Encrypted {
			enc = "encrypted"
		}
		rows = append(rows, []string{b.ID, b.CreatedAt.Local().Format("2006-01-02 15:04"), b.Kind, humanSize(b.SizeBytes), b.Source, enc, here})
	}
	c.table([]string{"BACKUP", "CREATED", "CONTENTS", "SIZE", "SOURCE", "", ""}, rows)
	return nil
}

// backupFetch copies a backup from an offsite target into the local backups.
func (c *cli) backupFetch(ctx context.Context, args []string) error {
	fs := c.newFlags("backup fetch")
	target := fs.String("target", "", "offsite target name or id")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, list, err := c.backupListing(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	want := arg(pos, 1)
	if want == "" {
		return usagef("which backup? envoryx backup remote %s shows them", p.Slug)
	}
	t, err := offsiteTargetFor(list.OffsiteTargets, *target)
	if err != nil {
		return err
	}
	var remote struct {
		Backups []remoteBackup `json:"backups"`
	}
	if err := api.get(ctx, projectPath(p.ID, "offsite", t.ID), nil, &remote); err != nil {
		return err
	}
	key := ""
	for _, b := range remote.Backups {
		if b.ID == want || b.Key == want {
			key = b.Key
			break
		}
	}
	if key == "" {
		return fmt.Errorf("no backup %q of %s on %s (envoryx backup remote %s shows them)", want, p.Slug, t.Name, p.Slug)
	}
	var out struct {
		Backup backupInfo `json:"backup"`
	}
	if err := api.post(ctx, projectPath(p.ID, "offsite", t.ID, "fetch"), map[string]string{"key": key}, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Fetched as backup %s of %s (%s, %s). Restore it with: envoryx backup restore %s %s --yes\n", out.Backup.ID, p.Slug, out.Backup.contents(), humanSize(out.Backup.SizeBytes), p.Slug, out.Backup.ID)
	return nil
}

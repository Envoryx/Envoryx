// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Instance backups: Envoryx's own database and configuration, not the projects. All of
// it needs an admin token.

type instanceBackup struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
	Meta      struct {
		Envoryx string `json:"envoryx"`
		Schema  int    `json:"schema"`
		Note    string `json:"note"`
		KeyID   string `json:"keyId"`
	} `json:"meta"`
}

type instanceBackupList struct {
	Backups        []instanceBackup         `json:"backups"`
	PendingRestore *struct{ ID string }     `json:"pendingRestore"`
	CanRestart     bool                     `json:"canRestart"`
	Offsite        map[string][]offsiteCopy `json:"offsite"`
	OffsiteTargets []offsiteTarget          `json:"offsiteTargets"`
}

const instanceBackups = "/api/v1/instance/backups"

func (c *cli) instanceCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "", "backups", "list", "ls":
		return c.instanceList(ctx, rest)
	case "backup", "create":
		return c.instanceCreate(ctx, rest)
	case "download":
		return c.instanceDownload(ctx, rest)
	case "upload":
		return c.instanceUpload(ctx, rest)
	case "delete", "rm":
		return c.instanceDelete(ctx, rest)
	case "restore":
		return c.instanceRestore(ctx, rest)
	case "cancel-restore":
		return c.instanceCancelRestore(ctx, rest)
	case "offsite":
		return c.instanceOffsite(ctx, rest)
	case "remote":
		return c.instanceRemote(ctx, rest)
	case "fetch":
		return c.instanceFetch(ctx, rest)
	default:
		return usagef("unknown instance command %q", cmd)
	}
}

func (c *cli) instanceListing(ctx context.Context) (*client, instanceBackupList, error) {
	api, err := c.connect()
	if err != nil {
		return nil, instanceBackupList{}, err
	}
	var list instanceBackupList
	err = api.get(ctx, instanceBackups, nil, &list)
	return api, list, err
}

func (c *cli) instanceList(ctx context.Context, args []string) error {
	fs := c.newFlags("instance backups")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx instance backups")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	if c.json {
		api, err := c.connect()
		if err != nil {
			return err
		}
		return c.printRaw(ctx, api, instanceBackups, nil, "backups")
	}
	_, list, err := c.instanceListing(ctx)
	if err != nil {
		return err
	}
	if len(list.Backups) == 0 {
		c.printf("No instance backups yet. envoryx instance backup creates one.\n")
	} else {
		rows := make([][]string, 0, len(list.Backups))
		for _, b := range list.Backups {
			row := []string{b.ID, b.CreatedAt.Local().Format("2006-01-02 15:04"), b.Kind, humanSize(b.SizeBytes), b.Meta.Envoryx}
			if len(list.OffsiteTargets) > 0 {
				row = append(row, offsiteSummary(list.Offsite[b.ID]))
			}
			rows = append(rows, append(row, b.Meta.Note))
		}
		header := []string{"ID", "CREATED", "KIND", "SIZE", "ENVORYX"}
		if len(list.OffsiteTargets) > 0 {
			header = append(header, "OFFSITE")
		}
		c.table(append(header, "NOTE"), rows)
	}
	if list.PendingRestore != nil {
		c.printf("\nA restore of %s is scheduled for the next start (envoryx instance cancel-restore drops it).\n", list.PendingRestore.ID)
	}
	return nil
}

func (c *cli) instanceCreate(ctx context.Context, args []string) error {
	fs := c.newFlags("instance backup")
	note := fs.String("note", "", "note to store with the backup")
	offsite := fs.Bool("offsite", false, "copy it to every enabled offsite target")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx instance backup [--note TEXT] [--offsite]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	var out struct {
		Backup instanceBackup `json:"backup"`
	}
	if err := api.post(ctx, instanceBackups, map[string]string{"note": *note}, &out); err != nil {
		return err
	}
	var copies []offsiteCopy
	if *offsite {
		var up struct {
			Offsite []offsiteCopy `json:"offsite"`
		}
		if err := api.post(ctx, instanceBackups+"/"+url.PathEscape(out.Backup.ID)+"/offsite", map[string]any{"targets": []string{}}, &up); err != nil {
			return fmt.Errorf("the backup %s was created, but not copied offsite: %w", out.Backup.ID, err)
		}
		copies = up.Offsite
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Instance backup %s (%s)\n", out.Backup.ID, humanSize(out.Backup.SizeBytes))
	for _, o := range copies {
		c.printf("  Copying to %s in the background (envoryx instance backups shows how it went)\n", o.TargetName)
	}
	return nil
}

func (c *cli) instanceDownload(ctx context.Context, args []string) error {
	fs := c.newFlags("instance download")
	out := fs.String("o", "", "file to write (default: the archive's name, not over an existing file; \"-\" is stdout)")
	fs.StringVar(out, "output", "", "file to write")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("which backup? envoryx instance backups shows them")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	resp, err := api.request(ctx, http.MethodGet, instanceBackups+"/"+url.PathEscape(pos[0])+"/download", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The archive holds the instance's database: keep it to its owner.
	return c.saveDownload(resp, *out, "envoryx-"+pos[0]+".tar.gz", 0o600)
}

func (c *cli) instanceUpload(ctx context.Context, args []string) error {
	fs := c.newFlags("instance upload")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx instance upload <file.tar.gz>")
	}
	if _, err := os.Stat(pos[0]); err != nil {
		return fileError(err)
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			part, err := mw.CreateFormFile("file", filepath.Base(pos[0]))
			if err != nil {
				return err
			}
			if err := copyFile(pos[0], part); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()
	var out struct {
		Backup instanceBackup `json:"backup"`
	}
	err = api.upload(ctx, instanceBackups+"/upload", mw.FormDataContentType(), pr, &out)
	_ = pr.Close()
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Uploaded as instance backup %s (Envoryx %s, %s). %s\n",
		out.Backup.ID, out.Backup.Meta.Envoryx, humanSize(out.Backup.SizeBytes), restoreInBrowser)
	return nil
}

func (c *cli) instanceDelete(ctx context.Context, args []string) error {
	fs := c.newFlags("instance delete")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("which backup? envoryx instance backups shows them")
	}
	if !*yes {
		return usagef("deleting the instance backup %s cannot be undone; confirm with --yes", pos[0])
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	if err := api.do(ctx, http.MethodDelete, instanceBackups+"/"+url.PathEscape(pos[0]), nil, nil, nil); err != nil {
		return err
	}
	c.printf("Deleted the instance backup %s.\n", pos[0])
	return nil
}

// restoreInBrowser is where an uploaded or fetched instance backup is restored: the
// CLI's token may only restore the backups the instance made itself.
const restoreInBrowser = "Restore it in the web UI (Settings, Backups, Instance backups): an API token can't restore a backup from elsewhere."

// instanceRestore schedules a restore; the server restarts to apply it. --wait waits
// until a new server process answers.
func (c *cli) instanceRestore(ctx context.Context, args []string) error {
	fs := c.newFlags("instance restore")
	yes := fs.Bool("yes", false, "confirm")
	keyFile := fs.String("key-file", "", "the backup's secret key, when it isn't this instance's (\"-\" reads stdin)")
	wait := fs.Bool("wait", false, "wait until Envoryx is back")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("which backup? envoryx instance backups shows them")
	}
	if !*yes {
		return usagef("restoring replaces Envoryx's database and configuration (accounts, tokens and settings included) and restarts it; confirm with --yes")
	}
	key := ""
	if *keyFile != "" {
		b, err := c.readFileOrStdin(*keyFile)
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(b))
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	scheduledAt := time.Now()
	var out struct {
		Restarting bool `json:"restarting"`
	}
	if err := api.post(ctx, instanceBackups+"/"+url.PathEscape(pos[0])+"/restore", map[string]string{"confirm": "restore", "key": key}, &out); err != nil {
		if strings.Contains(err.Error(), "give that key") {
			return fmt.Errorf("%w (pass it with --key-file)", err)
		}
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Code == "session_required" {
			return fmt.Errorf("%s was uploaded or fetched from an offsite target, and API tokens can't restore such a backup: sign in to the web UI and restore it there (Settings, Backups, Instance backups)", pos[0])
		}
		return err
	}
	if !out.Restarting {
		c.printf("The restore of %s is scheduled; restart Envoryx to apply it.\n", pos[0])
		return nil
	}
	if !*wait {
		c.printf("The restore of %s is scheduled and Envoryx restarts to apply it.\n", pos[0])
		return nil
	}
	c.printf("The restore of %s is scheduled; waiting for Envoryx to restart…\n", pos[0])
	v, err := c.waitForRestart(ctx, api, scheduledAt)
	if err != nil {
		return err
	}
	c.printf("Envoryx %s is back. The restored database has its own accounts and tokens, so this token may no longer work.\n", v)
	return nil
}

// restartPoll is how often --wait asks whether the server is back.
var restartPoll = 2 * time.Second

// waitForRestart waits for a server process younger than the request that restarted it:
// its uptime says so even when the restart was too quick to see it go away.
func (c *cli) waitForRestart(ctx context.Context, api *client, since time.Time) (string, error) {
	if c.timeout <= 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}
	for {
		var health struct {
			Status  string `json:"status"`
			Version string `json:"version"`
			Uptime  int    `json:"uptime"`
		}
		if err := api.get(ctx, "/api/v1/health", nil, &health); err == nil && health.Status != "unavailable" &&
			time.Duration(health.Uptime)*time.Second <= time.Since(since) {
			return health.Version, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", errors.New("Envoryx did not come back in time; its log says why")
			}
			return "", ctx.Err()
		case <-time.After(restartPoll):
		}
	}
}

func (c *cli) instanceCancelRestore(ctx context.Context, args []string) error {
	fs := c.newFlags("instance cancel-restore")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx instance cancel-restore")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	if err := api.do(ctx, http.MethodDelete, "/api/v1/instance/restore", nil, nil, nil); err != nil {
		return err
	}
	c.printf("No restore is scheduled any more.\n")
	return nil
}

func (c *cli) instanceOffsite(ctx context.Context, args []string) error {
	fs := c.newFlags("instance offsite")
	target := fs.String("target", "", "offsite target name or id (default: every enabled one)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("which backup? envoryx instance backups shows them")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, list, err := c.instanceListing(ctx)
	if err != nil {
		return err
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
	if err := api.post(ctx, instanceBackups+"/"+url.PathEscape(pos[0])+"/offsite", body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Offsite)
	}
	for _, o := range out.Offsite {
		c.printf("Copying %s to %s in the background\n", pos[0], o.TargetName)
	}
	return nil
}

func (c *cli) instanceRemoteListing(ctx context.Context, target string) (*client, offsiteTarget, []remoteBackup, instanceBackupList, error) {
	api, list, err := c.instanceListing(ctx)
	if err != nil {
		return nil, offsiteTarget{}, nil, list, err
	}
	t, err := offsiteTargetFor(list.OffsiteTargets, target)
	if err != nil {
		return nil, offsiteTarget{}, nil, list, err
	}
	var out struct {
		Backups []remoteBackup `json:"backups"`
	}
	err = api.get(ctx, "/api/v1/offsite/targets/"+url.PathEscape(t.ID)+"/instance", nil, &out)
	return api, t, out.Backups, list, err
}

func (c *cli) instanceRemote(ctx context.Context, args []string) error {
	fs := c.newFlags("instance remote")
	target := fs.String("target", "", "offsite target name or id")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx instance remote [--target NAME]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	_, t, remote, list, err := c.instanceRemoteListing(ctx, *target)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(remote)
	}
	if len(remote) == 0 {
		c.printf("No instance backups on %s.\n", t.Name)
		return nil
	}
	local := map[string]bool{}
	for _, b := range list.Backups {
		local[b.ID] = true
	}
	rows := make([][]string, 0, len(remote))
	for _, b := range remote {
		here, enc := "", ""
		if local[b.ID] {
			here = "also here"
		}
		if b.Encrypted {
			enc = "encrypted"
		}
		rows = append(rows, []string{b.ID, b.CreatedAt.Local().Format("2006-01-02 15:04"), b.Kind, humanSize(b.SizeBytes), enc, here})
	}
	c.table([]string{"BACKUP", "CREATED", "KIND", "SIZE", "", ""}, rows)
	return nil
}

func (c *cli) instanceFetch(ctx context.Context, args []string) error {
	fs := c.newFlags("instance fetch")
	target := fs.String("target", "", "offsite target name or id")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("which backup? envoryx instance remote shows them")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, t, remote, _, err := c.instanceRemoteListing(ctx, *target)
	if err != nil {
		return err
	}
	key := ""
	for _, b := range remote {
		if b.ID == pos[0] || b.Key == pos[0] {
			key = b.Key
			break
		}
	}
	if key == "" {
		return fmt.Errorf("no instance backup %q on %s (envoryx instance remote shows them)", pos[0], t.Name)
	}
	var out struct {
		Backup instanceBackup `json:"backup"`
	}
	if err := api.post(ctx, "/api/v1/offsite/targets/"+url.PathEscape(t.ID)+"/instance/fetch", map[string]string{"key": key}, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Backup)
	}
	c.printf("Fetched as instance backup %s (%s). %s\n", out.Backup.ID, humanSize(out.Backup.SizeBytes), restoreInBrowser)
	return nil
}

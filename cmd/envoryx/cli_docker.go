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

// The clean-up of the Docker page: runtime images no project uses any more, and what
// projects left behind (orphans). Admin token.

type unusedImage struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
	Size int64    `json:"size"`
}

type orphan struct {
	Type        string    `json:"type"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ProjectName string    `json:"projectName"`
	State       string    `json:"state"`
	Created     time.Time `json:"created"`
	Unlabelled  bool      `json:"unlabelled"`
}

type reconcileReport struct {
	At      time.Time `json:"at"`
	Orphans []orphan  `json:"orphans"`
	Issues  []struct {
		ProjectName string `json:"projectName"`
		Severity    string `json:"severity"`
		Message     string `json:"message"`
	} `json:"issues"`
	Error string `json:"error"`
}

func (c *cli) dockerCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "":
		return errCLIUsage
	case "images":
		return c.dockerImages(ctx, rest)
	case "prune":
		return c.dockerPrune(ctx, rest)
	case "orphans":
		return c.dockerOrphans(ctx, rest)
	case "remove", "rm":
		return c.dockerRemove(ctx, rest)
	default:
		return usagef("unknown docker command %q", cmd)
	}
}

func (c *cli) unusedImages(ctx context.Context, api *client) ([]unusedImage, error) {
	var body struct {
		Images []unusedImage `json:"images"`
	}
	err := api.get(ctx, "/api/v1/docker/images/unused", nil, &body)
	return body.Images, err
}

func imageName(img unusedImage) string {
	switch len(img.Tags) {
	case 0:
	case 1:
		return img.Tags[0]
	default:
		return fmt.Sprintf("%s (+%d tags)", img.Tags[0], len(img.Tags)-1)
	}
	id := strings.TrimPrefix(img.ID, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func (c *cli) dockerImages(ctx context.Context, args []string) error {
	fs := c.newFlags("docker images")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx docker images")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	images, err := c.unusedImages(ctx, api)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(images)
	}
	if len(images) == 0 {
		c.printf("Every Envoryx image is in use.\n")
		return nil
	}
	var total int64
	rows := make([][]string, 0, len(images))
	for _, img := range images {
		total += img.Size
		rows = append(rows, []string{imageName(img), humanSize(img.Size)})
	}
	c.table([]string{"UNUSED IMAGE", "SIZE"}, rows)
	c.printf("\n%s in all; envoryx docker prune --yes removes them.\n", humanSize(total))
	return nil
}

func (c *cli) dockerPrune(ctx context.Context, args []string) error {
	fs := c.newFlags("docker prune")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx docker prune --yes")
	}
	if !*yes {
		return usagef("pruning removes the Envoryx images no project uses (envoryx docker images lists them); confirm with --yes")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		Result struct {
			Removed        []unusedImage `json:"removed"`
			ReclaimedBytes int64         `json:"reclaimedBytes"`
			Errors         []string      `json:"errors"`
		} `json:"result"`
	}
	if err := api.post(ctx, "/api/v1/docker/images/prune", nil, &body); err != nil {
		return err
	}
	res := body.Result
	if c.json {
		return c.printJSON(res)
	}
	for _, img := range res.Removed {
		c.printf("Removed %s\n", imageName(img))
	}
	c.printf("%d image%s removed, %s freed.\n", len(res.Removed), plural(len(res.Removed)), humanSize(res.ReclaimedBytes))
	if len(res.Errors) > 0 {
		return fmt.Errorf("some images stayed: %s", strings.Join(res.Errors, "; "))
	}
	return nil
}

func (c *cli) reconcileReport(ctx context.Context, api *client, refresh bool) (reconcileReport, error) {
	var body struct {
		Report reconcileReport `json:"report"`
	}
	var err error
	if refresh {
		err = api.post(ctx, "/api/v1/system/reconcile", nil, &body)
	} else {
		err = api.get(ctx, "/api/v1/system/reconcile", nil, &body)
	}
	return body.Report, err
}

// dockerOrphans lists what projects left behind, as the last reconcile pass (every
// minute) found it; --refresh runs a pass first.
func (c *cli) dockerOrphans(ctx context.Context, args []string) error {
	fs := c.newFlags("docker orphans")
	refresh := fs.Bool("refresh", false, "look again first instead of using the last pass")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx docker orphans [--refresh]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	report, err := c.reconcileReport(ctx, api, *refresh)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(report.Orphans)
	}
	if report.Error != "" {
		return fmt.Errorf("the last check failed: %s", report.Error)
	}
	if len(report.Orphans) == 0 {
		c.printf("Nothing left behind.\n")
	} else {
		rows := make([][]string, 0, len(report.Orphans))
		unlabelled := false
		for _, o := range report.Orphans {
			from := o.ProjectName
			if o.Unlabelled {
				from += " *"
				unlabelled = true
			}
			created := ""
			if !o.Created.IsZero() {
				created = o.Created.Local().Format("2006-01-02")
			}
			rows = append(rows, []string{o.Type, o.Name, from, o.State, created})
		}
		c.table([]string{"TYPE", "NAME", "PROJECT", "STATE", "CREATED"}, rows)
		c.printf("\nenvoryx docker remove <type> <name> --yes removes one.\n")
		if unlabelled {
			c.printf("* without an instance label: an older Envoryx or another instance on this host made it, so check before removing.\n")
		}
	}
	for _, is := range report.Issues {
		c.printf("%s: %s: %s\n", strings.ToUpper(is.Severity), is.ProjectName, is.Message)
	}
	return nil
}

func (c *cli) dockerRemove(ctx context.Context, args []string) error {
	fs := c.newFlags("docker remove")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx docker remove container|network|volume <name> --yes (envoryx docker orphans lists them)")
	}
	typ, want := pos[0], pos[1]
	if !*yes {
		return usagef("removing the %s %s deletes it for good; confirm with --yes", typ, want)
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	report, err := c.reconcileReport(ctx, api, false)
	if err != nil {
		return err
	}
	id := ""
	for _, o := range report.Orphans {
		if o.Type == typ && (o.Name == want || o.ID == want || (len(want) >= 12 && strings.HasPrefix(o.ID, want))) {
			id = o.ID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("no orphaned %s %q (envoryx docker orphans lists them)", typ, want)
	}
	if err := api.post(ctx, "/api/v1/docker/orphans/remove", map[string]string{"type": typ, "id": id}, nil); err != nil {
		return err
	}
	c.printf("Removed the %s %s.\n", typ, want)
	return nil
}

// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"strings"
)

// projectBranches lists a project's branch environments.
func (c *cli) projectBranches(ctx context.Context, args []string) error {
	fs := c.newFlags("project branches")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx project branches <project>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, pos[0])
	if err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	if c.json {
		return c.printRaw(ctx, api, projectPath(p.ID, "branches"), nil, "environments")
	}
	var body struct {
		Environments []projectSummary `json:"environments"`
	}
	if err := api.get(ctx, projectPath(p.ID, "branches"), nil, &body); err != nil {
		return err
	}
	if len(body.Environments) == 0 {
		c.printf("%s has no branch environments. Create one with: envoryx project branch %s <branch>\n", p.Slug, p.Slug)
		return nil
	}
	rows := make([][]string, 0, len(body.Environments))
	for _, e := range body.Environments {
		deploy, commit := "", ""
		if s := e.BranchState; s != nil {
			deploy, commit = s.DeployStatus, s.Commit
			if len(commit) > 8 {
				commit = commit[:8]
			}
		}
		rows = append(rows, []string{e.Git.Branch, e.Slug, projectState(e), commit, deploy, e.URL()})
	}
	c.table([]string{"BRANCH", "SLUG", "STATE", "COMMIT", "DEPLOY", "URL"}, rows)
	return nil
}

// projectBranch creates a branch environment.
func (c *cli) projectBranch(ctx context.Context, args []string) error {
	fs := c.newFlags("project branch")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx project branch <project> <branch>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, pos[0])
	if err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		Project projectSummary `json:"project"`
	}
	// A branch environment is a copy of the project: files, a dump, a deploy.
	if err := api.post(ctx, projectPath(p.ID, "branches"), map[string]string{"branch": pos[1]}, &body); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(body.Project)
	}
	c.printf("Created %s (%s) on the branch %s\n", body.Project.Name, body.Project.Slug, pos[1])
	if u := body.Project.URL(); u != "" {
		c.printf("  %s\n", u)
	}
	c.printDeploy(body.Project)
	return nil
}

// projectDeploy pulls a branch environment and runs the deploy commands.
func (c *cli) projectDeploy(ctx context.Context, args []string) error {
	fs := c.newFlags("project deploy")
	noPull := fs.Bool("no-pull", false, "only run the deploy commands")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx project deploy <environment> [--no-pull]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, pos[0])
	if err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		Project projectSummary `json:"project"`
	}
	err = api.post(ctx, projectPath(p.ID, "deploy"), map[string]bool{"pull": !*noPull}, &body)
	if c.json && err == nil {
		return c.printJSON(body.Project)
	}
	if err != nil {
		// A failed deploy still recorded its output; show it before the error.
		if e, gerr := c.findProject(ctx, p.ID); gerr == nil {
			c.printDeploy(e)
		}
		return err
	}
	c.printDeploy(body.Project)
	return nil
}

// printDeploy shows the outcome of an environment's last deploy.
func (c *cli) printDeploy(p projectSummary) {
	s := p.BranchState
	if s == nil || s.DeployStatus == "" {
		return
	}
	commit := s.Commit
	if len(commit) > 8 {
		commit = commit[:8]
	}
	c.printf("Deploy %s at %s\n", s.DeployStatus, commit)
	if out := strings.TrimSpace(s.DeployOutput); out != "" && s.DeployStatus != "succeeded" {
		c.printf("%s\n", out)
	}
}

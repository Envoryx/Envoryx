// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type gitStatus struct {
	Configured bool   `json:"configured"`
	URL        string `json:"url"`
	Branch     string `json:"branch"`
	IsRepo     bool   `json:"isRepo"`
	Current    string `json:"currentBranch"`
	ShortHash  string `json:"shortHash"`
	Subject    string `json:"subject"`
	Author     string `json:"author"`
	Date       string `json:"date"`
	Dirty      int    `json:"dirty"`
	Remote     string `json:"remote"`
	Error      string `json:"error"`
}

type gitResult struct {
	Output   string    `json:"output"`
	ExitCode int       `json:"exitCode"`
	Status   gitStatus `json:"status"`
}

func (c *cli) gitCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIUsage
	case "", "status":
		return c.gitStatus(ctx, rest)
	case "clone":
		return c.gitRun(ctx, "clone", rest)
	case "pull":
		return c.gitRun(ctx, "pull", rest)
	case "checkout":
		return c.gitCheckout(ctx, rest)
	default:
		return usagef("unknown git command %q", cmd)
	}
}

func (c *cli) gitStatus(ctx context.Context, args []string) error {
	fs := c.newFlags("git status")
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
		return c.printRaw(ctx, api, projectPath(p.ID, "git"), nil, "git")
	}
	var body struct {
		Git gitStatus `json:"git"`
	}
	if err := api.get(ctx, projectPath(p.ID, "git"), nil, &body); err != nil {
		return err
	}
	c.printGitStatus(p, body.Git)
	return nil
}

func (c *cli) printGitStatus(p projectSummary, g gitStatus) {
	if !g.Configured && !g.IsRepo {
		c.printf("%s has no repository configured.\n", p.Slug)
		return
	}
	if g.URL != "" {
		c.printf("  Remote    %s\n", g.URL)
	}
	if g.Current != "" {
		c.printf("  Branch    %s\n", g.Current)
	}
	if g.ShortHash != "" {
		c.printf("  Commit    %s %s\n", g.ShortHash, g.Subject)
		c.printf("  Author    %s, %s\n", g.Author, g.Date)
	}
	if g.Dirty > 0 {
		c.printf("  Changes   %d uncommitted file%s\n", g.Dirty, plural(g.Dirty))
	}
	if g.Error != "" {
		c.printf("  Error     %s\n", g.Error)
	}
}

// gitRun performs clone or pull and prints git's own output, which is the part people
// read when something went wrong.
func (c *cli) gitRun(ctx context.Context, action string, args []string) error {
	fs := c.newFlags("git " + action)
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
	var body struct {
		Result gitResult `json:"result"`
	}
	if err := api.post(ctx, projectPath(p.ID, "git", action), struct{}{}, &body); err != nil {
		return gitFailure(err)
	}
	return c.printGitResult(p, body.Result)
}

func (c *cli) gitCheckout(ctx context.Context, args []string) error {
	fs := c.newFlags("git checkout")
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
	branch := arg(pos, 1)
	if branch == "" {
		return usagef("which branch? envoryx git checkout %s <branch>", p.Slug)
	}
	var body struct {
		Result gitResult `json:"result"`
	}
	if err := api.post(ctx, projectPath(p.ID, "git", "checkout"), map[string]string{"branch": branch}, &body); err != nil {
		return gitFailure(err)
	}
	return c.printGitResult(p, body.Result)
}

func (c *cli) printGitResult(p projectSummary, res gitResult) error {
	if c.json {
		return c.printJSON(res)
	}
	if out := strings.TrimSpace(res.Output); out != "" {
		fmt.Fprintln(c.errOut, out)
	}
	c.printGitStatus(p, res.Status)
	return nil
}

// gitFailure unwraps the 409 a failed git command produces: its message is the reason,
// its body carries what git itself printed.
func gitFailure(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "git_failed" {
		return err
	}
	var body struct {
		Result gitResult `json:"result"`
	}
	if json.Unmarshal(apiErr.Body, &body) == nil {
		if out := strings.TrimSpace(body.Result.Output); out != "" {
			return fmt.Errorf("%s\n%s", apiErr.Message, out)
		}
	}
	return err
}

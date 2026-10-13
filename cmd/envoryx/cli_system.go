// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Read-only views of the instance for admins: the diagnostics and the audit log.

type diagnosticCheck struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint"`
}

// diagnostics prints the checks of Settings → Diagnostics. It fails when a check is in
// error, or with --strict also on a warning, so a monitoring job can run it as it is.
func (c *cli) diagnostics(ctx context.Context, args []string) error {
	fs := c.newFlags("diagnostics")
	all := fs.Bool("all", false, "list the checks that are fine too")
	strict := fs.Bool("strict", false, "fail on warnings as well")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx diagnostics [--all] [--strict]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		Checks  []diagnosticCheck `json:"checks"`
		Summary map[string]int    `json:"summary"`
	}
	if err := api.get(ctx, "/api/v1/system/diagnostics", nil, &body); err != nil {
		return err
	}
	if c.json {
		if err := c.printJSON(body.Checks); err != nil {
			return err
		}
	} else {
		// One block per check: the details and hints are sentences, too long for columns.
		shown := 0
		for _, ch := range body.Checks {
			if ch.Status == "ok" && !*all {
				continue
			}
			c.printf("%-7s  %s (%s)\n", strings.ToUpper(ch.Status), ch.Title, ch.Category)
			if ch.Detail != "" {
				c.printf("         %s\n", ch.Detail)
			}
			if ch.Hint != "" && ch.Status != "ok" {
				c.printf("         → %s\n", ch.Hint)
			}
			shown++
		}
		if shown > 0 {
			c.printf("\n")
		}
		c.printf("%d ok, %d info, %d warnings, %d errors\n", body.Summary["ok"], body.Summary["info"], body.Summary["warning"], body.Summary["error"])
	}
	failed := body.Summary["error"]
	if *strict {
		failed += body.Summary["warning"]
	}
	if failed > 0 {
		if failed == 1 {
			return fmt.Errorf("1 check needs attention")
		}
		return fmt.Errorf("%d checks need attention", failed)
	}
	return nil
}

type auditEntry struct {
	ID         string          `json:"id"`
	CreatedAt  time.Time       `json:"createdAt"`
	Username   string          `json:"username"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Details    json.RawMessage `json:"details"`
	IP         string          `json:"ip"`
}

// target names what an entry is about: the name its details keep, else its id.
func (e auditEntry) target() string {
	var d map[string]any
	_ = json.Unmarshal(e.Details, &d)
	for _, key := range []string{"name", "username", "job"} {
		if s, ok := d[key].(string); ok && s != "" {
			return s
		}
	}
	return e.TargetID
}

// audit lists the audit log, newest first, with the filters of the web interface; with
// --export it streams every matching entry as CSV or JSON Lines.
func (c *cli) audit(ctx context.Context, args []string) error {
	fs := c.newFlags("audit")
	search := fs.String("search", "", "text anywhere in the entry")
	user := fs.String("user", "", "only this account (tokens count as their account)")
	var actions stringList
	fs.Var(&actions, "action", "action prefix, e.g. project. or auth.login (repeatable)")
	projectName := fs.String("project", "", "only entries about this project")
	since := fs.String("since", "", "from this date (2026-10-01) or time (RFC 3339)")
	until := fs.String("until", "", "up to and including this date, or up to this time")
	limit := fs.Int("limit", 50, "how many entries (at most 1000)")
	export := fs.String("export", "", "every matching entry as csv or jsonl")
	output := fs.String("o", "", "file for --export (default: its name from the server, not over an existing file; \"-\" is stdout)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx audit [--user NAME] [--action PREFIX] [--project P] [--since DATE] [--until DATE] [--search TEXT] [--limit N] [--export csv|jsonl [-o FILE]]")
	}
	if *export != "" && *export != "csv" && *export != "jsonl" {
		return usagef("--export takes csv or jsonl")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("q", *search)
	set("user", *user)
	set("since", *since)
	set("until", *until)
	for _, a := range actions {
		q.Add("action", a)
	}
	if *projectName != "" {
		id := *projectName
		// A deleted project's id still finds its entries; a name needs the project.
		if p, err := c.findProject(ctx, *projectName); err == nil {
			id = p.ID
		} else if !looksLikeID(id) {
			return err
		}
		q.Set("project", id)
	}
	if *export != "" {
		q.Set("format", *export)
		return c.auditExport(ctx, api, q, *output)
	}
	q.Set("limit", strconv.Itoa(*limit))
	var body struct {
		Entries []auditEntry `json:"entries"`
		Next    string       `json:"next"`
	}
	if err := api.get(ctx, "/api/v1/audit", q, &body); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(body.Entries)
	}
	if len(body.Entries) == 0 {
		c.printf("No audit entries match.\n")
		return nil
	}
	rows := make([][]string, 0, len(body.Entries))
	for _, e := range body.Entries {
		rows = append(rows, []string{e.CreatedAt.Local().Format("2006-01-02 15:04:05"), e.Username, e.Action, e.target(), e.IP})
	}
	c.table([]string{"TIME", "USER", "ACTION", "TARGET", "IP"}, rows)
	if body.Next != "" {
		c.printf("\nThere is more: raise --limit, narrow it down with --since/--until, or --export all of it.\n")
	}
	return nil
}

func (c *cli) auditExport(ctx context.Context, api *client, q url.Values, target string) error {
	resp, err := api.request(ctx, http.MethodGet, "/api/v1/audit/export", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.saveDownload(resp, target, "envoryx-audit."+q.Get("format"), 0o600)
}

// looksLikeID tells an id (a UUID) from a name.
func looksLikeID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

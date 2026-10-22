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
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ---- instance settings -------------------------------------------------------

// settingKey is one instance setting the CLI can change: its name on the command line,
// where it lives in the settings answer, and how a value becomes the PATCH body.
type settingKey struct {
	name string
	// field is the key in GET /settings and in PATCH /settings; nested is the key below
	// it for the log history and metrics objects.
	field, nested string
	kind          string // string, bool, int
	help          string
}

var settingKeys = []settingKey{
	{name: "public-host", field: "publicHost", kind: "string", help: "host for project links (the Docker host's address)"},
	{name: "base-domain", field: "baseDomain", kind: "string", help: "projects answer as <slug>.<base domain>"},
	{name: "force-https", field: "forceHttps", kind: "bool", help: "redirect project addresses to https"},
	{name: "projects-follow-envoryx", field: "projectsFollowEnvoryx", kind: "bool", help: "stop the projects with Envoryx and start them again"},
	{name: "new-projects-private", field: "newProjectsPrivate", kind: "bool", help: "new projects only let in people signed in to Envoryx"},
	{name: "shared-ide-backends", field: "sharedIdeBackends", kind: "bool", help: "one set of JetBrains backends for all projects"},
	{name: "shared-package-cache", field: "sharedPackageCache", kind: "bool", help: "one package cache for all projects"},
	{name: "xdebug-client-host", field: "xdebugClientHost", kind: "string", help: "the developer machine Xdebug connects back to"},
	{name: "folderview-folder", field: "folderViewFolder", kind: "string", help: "FolderView3 folder for the containers (Unraid)"},
	{name: "log-history", field: "logHistory", nested: "enabled", kind: "bool", help: "keep the containers' output"},
	{name: "log-history-days", field: "logHistory", nested: "retentionDays", kind: "int", help: "days of output to keep"},
	{name: "log-history-max-mb", field: "logHistory", nested: "maxMb", kind: "int", help: "size limit of the kept output"},
	{name: "metrics-days", field: "metrics", nested: "retentionDays", kind: "int", help: "days of resource history to keep"},
}

// The instance's SSH keys are left out on purpose: they open a shell in every project and
// would outlast the API token the CLI signs in with, so the API takes them from a browser
// session only (Settings → SSH in the web interface).

func settingByName(name string) (settingKey, bool) {
	for _, k := range settingKeys {
		if k.name == name {
			return k, true
		}
	}
	return settingKey{}, false
}

func (c *cli) settingsCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "", "show", "list":
		return c.settingsShow(ctx, rest)
	case "set":
		return c.settingsSet(ctx, rest)
	default:
		return usagef("unknown settings command %q", cmd)
	}
}

// settingValue reads one setting out of the settings answer as text.
func settingValue(body map[string]json.RawMessage, k settingKey) string {
	raw := body[k.field]
	if k.nested != "" {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return ""
		}
		raw = obj[k.nested]
	}
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case bool:
		return map[bool]string{true: "on", false: "off"}[v]
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func (c *cli) settingsShow(ctx context.Context, args []string) error {
	fs := c.newFlags("settings")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: envoryx settings")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	if c.json {
		var body any
		if err := api.get(ctx, "/api/v1/settings", nil, &body); err != nil {
			return err
		}
		return c.printJSON(body)
	}
	var body map[string]json.RawMessage
	if err := api.get(ctx, "/api/v1/settings", nil, &body); err != nil {
		return err
	}
	var locked []string
	_ = json.Unmarshal(body["lockedSettings"], &locked)
	rows := make([][]string, 0, len(settingKeys))
	for _, k := range settingKeys {
		note := ""
		if slices.Contains(locked, k.field) {
			note = "fixed by the hoster"
		}
		v := settingValue(body, k)
		if v == "" {
			v = "-"
		}
		rows = append(rows, []string{k.name, v, note})
	}
	c.table([]string{"SETTING", "VALUE", ""}, rows)
	c.printf("\nenvoryx settings set NAME=VALUE changes one; an empty value clears a text setting.\n")
	return nil
}

func (c *cli) settingsSet(ctx context.Context, args []string) error {
	fs := c.newFlags("settings set")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		var names []string
		for _, k := range settingKeys {
			names = append(names, fmt.Sprintf("  %-24s %s", k.name, k.help))
		}
		return usagef("usage: envoryx settings set NAME=VALUE …\n%s", strings.Join(names, "\n"))
	}
	body := map[string]any{}
	nested := map[string]map[string]any{}
	var changed []string
	for _, a := range pos {
		name, value, ok := strings.Cut(a, "=")
		if !ok {
			return usagef("settings set expects NAME=VALUE, got %q", a)
		}
		k, ok := settingByName(name)
		if !ok {
			return usagef("unknown setting %q (envoryx settings lists them)", name)
		}
		var v any
		switch k.kind {
		case "bool":
			b, err := parseOnOff(value)
			if err != nil {
				return usagef("%s: %v", name, err)
			}
			v = b
		case "int":
			n, err := strconv.Atoi(value)
			if err != nil {
				return usagef("%s takes a number, not %q", name, value)
			}
			v = n
		default:
			v = value
		}
		if k.nested != "" {
			if nested[k.field] == nil {
				nested[k.field] = map[string]any{}
			}
			nested[k.field][k.nested] = v
		} else {
			body[k.field] = v
		}
		changed = append(changed, name)
	}
	for field, obj := range nested {
		// The resource history is a plain number in the request.
		if field == "metrics" {
			body["metricsRetentionDays"] = obj["retentionDays"]
			continue
		}
		body[field] = obj
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	var out map[string]json.RawMessage
	if err := api.do(ctx, http.MethodPatch, "/api/v1/settings", nil, body, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out)
	}
	sort.Strings(changed)
	for _, name := range changed {
		k, _ := settingByName(name)
		v := settingValue(out, k)
		if v == "" {
			v = "(empty)"
		}
		c.printf("%s = %s\n", name, v)
	}
	return nil
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("on or off, not %q", s)
}

// ---- project domains ---------------------------------------------------------

type domainInfo struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Default  bool   `json:"default"`
}

func (c *cli) domainCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "", "list", "ls":
		return c.domainList(ctx, rest)
	case "add":
		return c.domainAdd(ctx, rest)
	case "remove", "rm", "delete":
		return c.domainRemove(ctx, rest)
	default:
		return usagef("unknown domain command %q", cmd)
	}
}

func (c *cli) domains(ctx context.Context, api *client, p projectSummary) ([]domainInfo, error) {
	var body struct {
		Domains []domainInfo `json:"domains"`
	}
	err := api.get(ctx, projectPath(p.ID, "domains"), nil, &body)
	return body.Domains, err
}

func (c *cli) domainList(ctx context.Context, args []string) error {
	fs := c.newFlags("domain list")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx domain list <project>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	list, err := c.domains(ctx, api, p)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(list)
	}
	rows := make([][]string, 0, len(list))
	for _, d := range list {
		note := ""
		if d.Default {
			note = "default"
		}
		rows = append(rows, []string{d.Hostname, note})
	}
	c.table([]string{"DOMAIN", ""}, rows)
	return nil
}

func (c *cli) domainAdd(ctx context.Context, args []string) error {
	fs := c.newFlags("domain add")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx domain add <project> <hostname>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Domain domainInfo `json:"domain"`
	}
	if err := api.post(ctx, projectPath(p.ID, "domains"), map[string]string{"hostname": pos[1]}, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Domain)
	}
	c.printf("%s now also answers as %s.\n", p.Slug, out.Domain.Hostname)
	return nil
}

func (c *cli) domainRemove(ctx context.Context, args []string) error {
	fs := c.newFlags("domain remove")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx domain remove <project> <hostname>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	list, err := c.domains(ctx, api, p)
	if err != nil {
		return err
	}
	for _, d := range list {
		if !strings.EqualFold(d.Hostname, pos[1]) && d.ID != pos[1] {
			continue
		}
		if d.Default {
			return fmt.Errorf("%s is the project's own address under the base domain; it can't be removed", d.Hostname)
		}
		if err := api.do(ctx, http.MethodDelete, projectPath(p.ID, "domains", url.PathEscape(d.ID)), nil, nil, nil); err != nil {
			return err
		}
		c.printf("%s no longer answers as %s.\n", p.Slug, d.Hostname)
		return nil
	}
	return fmt.Errorf("%s has no domain %q (envoryx domain list %s shows them)", p.Slug, pos[1], p.Slug)
}

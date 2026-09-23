// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const cliUsage = `Usage: envoryx <command> [flags]

Work with a running Envoryx over its REST API – from an SSH session, a script or a
CI job. The address and an API token (web interface → Settings → API tokens) come
from "envoryx login", from the environment or from the flags below.

Projects:
  project list                          every project with its state and URL
  project show <project>                services, ports, git and backups
  project create <name> [flags]         create a project ("project create --help")
  project duplicate <project> <name>    copy it with config, files and database
  project rename <project> <name> --yes rename it and everything derived from it
  project start|stop|restart <project>
  project delete <project> --yes        remove it (--delete-files removes the files too)
  project logs <project> [flags]        recent output, --follow keeps reading
  project exec <project> -- <cmd…>      run a command in a container
  project run <project> [action]        run a catalogue action (composer install …)

Backups:
  backup list <project>
  backup create <project> [flags]       database + files by default
  backup restore <project> <backup> --yes
  backup download <project> <backup> [-o FILE]
  backup delete <project> <backup> --yes

Databases:
  db snapshot <project> [--note TEXT]   dump the database and nothing else
  db snapshots <project>                the snapshots there are
  db restore <project> <snapshot> --yes put one back
  db clone <project> --from <project> --yes
                                        replace its database with another's

Git:
  git status <project>
  git clone <project>                   clone the configured repository
  git pull <project>
  git checkout <project> <branch>

Access:
  login [--url URL] [--token TOKEN]     remember the server and the token
  logout                                forget them again
  whoami                                the account and what the token may do

Server (run on the host or inside the container):
  serve | healthcheck | admin … | version

Global flags:
  --url URL        Envoryx address, e.g. https://envoryx.example.com (ENVORYX_URL)
  --token TOKEN    API token (ENVORYX_TOKEN); "-" reads it from stdin
  --ca-cert FILE   trust this certificate authority (ENVORYX_CA_CERT)
  --insecure       do not verify the server certificate (ENVORYX_INSECURE)
  --json           print the API's JSON instead of tables
  --timeout D      give up after this long, e.g. 30s (default: wait, Ctrl+C stops)

A project is named by its name, its slug or its id. Commands exit 0 on success, 1 on
failure and 2 on a usage error; "project exec" passes the command's own exit code on.
`

// errCLIUsage asks the caller to print the usage text and exit with 2.
var errCLIUsage = errors.New("usage")

// usageError is a usage error with something more specific to say than the whole usage
// text – a missing argument, an unknown subcommand.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }
func (e *usageError) Is(target error) bool {
	return target == errCLIUsage
}

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// exitCodeError carries the exit code of a command run inside a container, so
// "envoryx project exec" can hand it to the shell that called it.
type exitCodeError struct {
	code int
}

func (e *exitCodeError) Error() string { return fmt.Sprintf("command exited with %d", e.code) }

// cliConfig is what the CLI needs to reach a server. It is read from the file written by
// "envoryx login" and overridden by the environment and the global flags, in that order.
type cliConfig struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	CACert   string `json:"caCert,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// cli is one CLI invocation: resolved configuration, where output goes, and the lazily
// built API client. Tests drive it with their own writers and a test server's address.
type cli struct {
	cfg     cliConfig
	out     io.Writer
	errOut  io.Writer
	stdin   io.Reader
	json    bool
	timeout time.Duration
	// flags holds the global flags as given, empty when they were not.
	flags cliConfig
	api   *client
	// projects caches the project list within one command (name → id resolution).
	projects []projectSummary
}

// cliCommand runs one CLI command and returns the process exit code.
func cliCommand(ctx context.Context, args []string) int {
	c := &cli{out: os.Stdout, errOut: os.Stderr, stdin: os.Stdin}
	err := c.run(ctx, args)
	var exit *exitCodeError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		// An exit code the engine could not determine (-1) must not become a process
		// exit code of its own; plain failure is the honest answer.
		if exit.code < 1 || exit.code > 255 {
			fmt.Fprintln(c.errOut, "envoryx:", err)
			return 1
		}
		return exit.code
	case errors.Is(err, errCLIUsage):
		// A specific complaint ("which project?") is more use than the whole usage
		// text, which stays for the bare command and for --help.
		var specific *usageError
		if errors.As(err, &specific) {
			fmt.Fprintf(c.errOut, "envoryx: %s\nSee \"envoryx help\".\n", specific.msg)
			return 2
		}
		fmt.Fprint(c.errOut, cliUsage)
		return 2
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(c.errOut, "envoryx: cancelled")
		return 1
	default:
		fmt.Fprintln(c.errOut, "envoryx:", err)
		return 1
	}
}

func (c *cli) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errCLIUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "login":
		return c.login(ctx, rest)
	case "logout":
		return c.logout(rest)
	case "whoami":
		return c.whoami(ctx, rest)
	case "project", "projects":
		return c.projectCommand(ctx, rest)
	case "backup", "backups":
		return c.backupCommand(ctx, rest)
	case "db", "database":
		return c.dbCommand(ctx, rest)
	case "git":
		return c.gitCommand(ctx, rest)
	default:
		return usagef("unknown command %q", cmd)
	}
}

// ---- flags and configuration -------------------------------------------------

// newFlags returns a flag set carrying the global flags; every command adds its own.
func (c *cli) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.flags.URL, "url", "", "Envoryx address")
	fs.StringVar(&c.flags.Token, "token", "", "API token")
	fs.StringVar(&c.flags.CACert, "ca-cert", "", "certificate authority to trust")
	fs.BoolVar(&c.flags.Insecure, "insecure", false, "do not verify the server certificate")
	fs.BoolVar(&c.json, "json", false, "print JSON")
	fs.DurationVar(&c.timeout, "timeout", 0, "give up after this long")
	return fs
}

// parse reads flags that may stand before, between or after the positional arguments,
// which is how people actually type them ("project start shop --json").
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errCLIUsage
			}
			return nil, usagef("%v", err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// splitCommand takes the subcommand off the front of a group's arguments.
func splitCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

// arg returns the nth positional argument, or "" when it was not given.
func arg(args []string, n int) string {
	if n < len(args) {
		return args[n]
	}
	return ""
}

func cliConfigPath() string {
	if p := os.Getenv("ENVORYX_CLI_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "envoryx-cli.json"
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "envoryx", "cli.json")
}

func loadCLIConfig() cliConfig {
	var cfg cliConfig
	b, err := os.ReadFile(cliConfigPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

func saveCLIConfig(cfg cliConfig) error {
	path := cliConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// The file holds a token: keep it readable by its owner only.
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// resolveConfig merges the stored configuration with the environment and the flags.
func (c *cli) resolveConfig() error {
	cfg := loadCLIConfig()
	if v := os.Getenv("ENVORYX_URL"); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv("ENVORYX_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("ENVORYX_CA_CERT"); v != "" {
		cfg.CACert = v
	}
	if v := os.Getenv("ENVORYX_INSECURE"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Insecure = true
	}
	if c.flags.URL != "" {
		cfg.URL = c.flags.URL
	}
	if c.flags.Token != "" {
		cfg.Token = c.flags.Token
	}
	if c.flags.CACert != "" {
		cfg.CACert = c.flags.CACert
	}
	if c.flags.Insecure {
		cfg.Insecure = true
	}
	if cfg.Token == "-" {
		token, err := io.ReadAll(io.LimitReader(c.stdin, 4096))
		if err != nil {
			return fmt.Errorf("reading the token from stdin: %w", err)
		}
		cfg.Token = strings.TrimSpace(string(token))
	}
	if cfg.URL == "" {
		cfg.URL = localServerURL()
	}
	if !strings.Contains(cfg.URL, "://") {
		cfg.URL = "https://" + cfg.URL
	}
	c.cfg = cfg
	return nil
}

// localServerURL is the address of a server on this machine, derived from ENVORYX_LISTEN
// the way the health check does. It makes "docker exec envoryx envoryx project list"
// work with nothing but a token.
func localServerURL() string {
	addr := os.Getenv("ENVORYX_LISTEN")
	if addr == "" {
		addr = ":8787"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = strings.TrimPrefix(addr, ":")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// connect resolves the configuration and returns the API client.
func (c *cli) connect() (*client, error) {
	if c.api != nil {
		return c.api, nil
	}
	if err := c.resolveConfig(); err != nil {
		return nil, err
	}
	if c.cfg.Token == "" {
		return nil, errors.New("no API token: run \"envoryx login\", or set ENVORYX_TOKEN (create one under Settings → API tokens)")
	}
	api, err := newClient(c.cfg)
	if err != nil {
		return nil, err
	}
	c.api = api
	return api, nil
}

// context applies --timeout to one command.
func (c *cli) context(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// ---- output ------------------------------------------------------------------

// printJSON writes a value as indented JSON; --json hands the API's own answer on.
func (c *cli) printJSON(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printRaw prints one field of an API answer as the server sent it, so --json hands a
// script the whole object instead of the subset the tables need.
func (c *cli) printRaw(ctx context.Context, api *client, path string, query url.Values, key string) error {
	var body map[string]json.RawMessage
	if err := api.get(ctx, path, query, &body); err != nil {
		return err
	}
	raw, ok := body[key]
	if !ok {
		return c.printJSON(body)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return c.printJSON(v)
}

// table prints aligned columns, the way "envoryx admin users" does.
func (c *cli) table(header []string, rows [][]string) {
	tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	if len(header) > 0 {
		fmt.Fprintln(tw, strings.Join(header, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

func (c *cli) printf(format string, a ...any) {
	fmt.Fprintf(c.out, format, a...)
}

// ---- projects ----------------------------------------------------------------

// projectSummary is the part of a project the CLI needs to find and describe it.
type projectSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Path         string   `json:"path"`
	DesiredState string   `json:"desiredState"`
	Lifecycle    string   `json:"lifecycle"`
	LastError    string   `json:"lastError"`
	HTTPPort     int      `json:"httpPort"`
	AppService   string   `json:"appService"`
	Serves       string   `json:"serves"`
	Hostnames    []string `json:"hostnames"`
	DevHostname  string   `json:"devHostname"`
	Status       struct {
		State    string   `json:"state"`
		Warnings []string `json:"warnings"`
		Services []struct {
			Kind    string `json:"kind"`
			Version string `json:"version"`
			Image   string `json:"image"`
			State   string `json:"state"`
			Running bool   `json:"running"`
			Health  string `json:"health"`
			Ports   []struct {
				HostIP        string `json:"hostIp"`
				HostPort      int    `json:"hostPort"`
				ContainerPort int    `json:"containerPort"`
			} `json:"ports"`
		} `json:"services"`
	} `json:"status"`
	Git struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"git"`
}

// URL is the project's address, as far as the CLI can tell without asking the settings.
func (p projectSummary) URL() string {
	if len(p.Hostnames) > 0 {
		return "https://" + p.Hostnames[0]
	}
	if p.HTTPPort > 0 {
		return fmt.Sprintf("port %d", p.HTTPPort)
	}
	return ""
}

func (c *cli) listProjects(ctx context.Context) ([]projectSummary, error) {
	if c.projects != nil {
		return c.projects, nil
	}
	api, err := c.connect()
	if err != nil {
		return nil, err
	}
	var body struct {
		Projects []projectSummary `json:"projects"`
	}
	if err := api.get(ctx, "/api/v1/projects", nil, &body); err != nil {
		return nil, err
	}
	c.projects = body.Projects
	return body.Projects, nil
}

// findProject resolves what the user typed – an id, a slug or a name – to one project.
// Tokens limited to particular projects only ever see theirs, so the same name resolves
// differently for different tokens, which is the point.
func (c *cli) findProject(ctx context.Context, want string) (projectSummary, error) {
	if strings.TrimSpace(want) == "" {
		return projectSummary{}, usagef("which project?")
	}
	projects, err := c.listProjects(ctx)
	if err != nil {
		return projectSummary{}, err
	}
	var matches []projectSummary
	for _, p := range projects {
		if p.ID == want || p.Slug == want || strings.EqualFold(p.Name, want) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return projectSummary{}, fmt.Errorf("no project %q (envoryx project list shows them all)", want)
	default:
		names := make([]string, 0, len(matches))
		for _, p := range matches {
			names = append(names, p.Slug+" ("+p.ID+")")
		}
		return projectSummary{}, fmt.Errorf("%q matches several projects: %s – use the slug or the id", want, strings.Join(names, ", "))
	}
}

// projectPath builds an API path below a project.
func projectPath(id string, rest ...string) string {
	p := "/api/v1/projects/" + url.PathEscape(id)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// requireService rejects a service the project does not have before a request is sent:
// the API answers such a route with a plain 404 that does not say what is missing, while
// the project listing knows exactly which services exist.
func (c *cli) requireService(p projectSummary, kind string) error {
	if strings.HasPrefix(kind, "worker:") || len(p.Status.Services) == 0 {
		return nil
	}
	var known []string
	for _, s := range p.Status.Services {
		if s.Kind == kind {
			return nil
		}
		known = append(known, s.Kind)
	}
	return fmt.Errorf("%s has no %s service (it has %s)", p.Slug, kind, strings.Join(known, ", "))
}

// appService is the container a command runs in unless --service says otherwise: the
// project's application container, and the web server for a project without one.
func (p projectSummary) appService() string {
	if p.AppService != "" {
		return p.AppService
	}
	return "web"
}

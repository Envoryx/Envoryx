// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ---- workers -----------------------------------------------------------------

type workerInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Preset  string   `json:"preset"`
	Arg     string   `json:"arg"`
	Enabled bool     `json:"enabled"`
	Command []string `json:"command"`
}

type workerPresetInfo struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	ArgLabel    string `json:"argLabel"`
	ArgHint     string `json:"argHint"`
	Runtime     string `json:"runtime"`
}

type workerList struct {
	Workers         []workerInfo       `json:"workers"`
	Presets         []workerPresetInfo `json:"presets"`
	SuggestedPreset string             `json:"suggestedPreset"`
}

func (c *cli) workerCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "", "list", "ls":
		return c.workerList(ctx, rest)
	case "presets":
		return c.workerPresets(ctx, rest)
	case "add", "create":
		return c.workerAdd(ctx, rest)
	case "edit", "update":
		return c.workerEdit(ctx, rest, nil)
	case "enable", "disable":
		enabled := cmd == "enable"
		return c.workerEdit(ctx, rest, &enabled)
	case "restart":
		return c.workerRestart(ctx, rest)
	case "remove", "delete", "rm":
		return c.workerRemove(ctx, rest)
	default:
		return usagef("unknown worker command %q", cmd)
	}
}

func (c *cli) fetchWorkers(ctx context.Context, api *client, p projectSummary) (workerList, error) {
	var body workerList
	err := api.get(ctx, projectPath(p.ID, "workers"), nil, &body)
	return body, err
}

// findWorker resolves a worker by its name or id.
func (c *cli) findWorker(ctx context.Context, api *client, p projectSummary, want string) (workerInfo, error) {
	if want == "" {
		return workerInfo{}, usagef("which worker? envoryx worker list %s shows them", p.Slug)
	}
	list, err := c.fetchWorkers(ctx, api, p)
	if err != nil {
		return workerInfo{}, err
	}
	for _, w := range list.Workers {
		if w.ID == want || strings.EqualFold(w.Name, want) {
			return w, nil
		}
	}
	return workerInfo{}, fmt.Errorf("%s has no worker %q (envoryx worker list %s shows them)", p.Slug, want, p.Slug)
}

// workerState is the state of the worker's container as the project listing reports it.
func workerState(p projectSummary, w workerInfo) string {
	if !w.Enabled {
		return "disabled"
	}
	for _, s := range p.Status.Services {
		if s.Kind == "worker" && s.WorkerID == w.ID {
			if s.State != "" {
				return s.State
			}
			if s.Running {
				return "running"
			}
			return "stopped"
		}
	}
	return "stopped"
}

func (c *cli) workerList(ctx context.Context, args []string) error {
	fs := c.newFlags("worker list")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx worker list <project>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		return c.printRaw(ctx, api, projectPath(p.ID, "workers"), nil, "workers")
	}
	list, err := c.fetchWorkers(ctx, api, p)
	if err != nil {
		return err
	}
	if len(list.Workers) == 0 {
		hint := "<preset>"
		if list.SuggestedPreset != "" {
			hint = list.SuggestedPreset
		}
		c.printf("%s has no workers. Add one with: envoryx worker add %s <name> --preset %s\n", p.Slug, p.Slug, hint)
		return nil
	}
	rows := make([][]string, 0, len(list.Workers))
	for _, w := range list.Workers {
		rows = append(rows, []string{w.Name, w.Preset, workerState(p, w), strings.Join(w.Command, " ")})
	}
	c.table([]string{"NAME", "PRESET", "STATE", "COMMAND"}, rows)
	return nil
}

func (c *cli) workerPresets(ctx context.Context, args []string) error {
	fs := c.newFlags("worker presets")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx worker presets <project>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		return c.printRaw(ctx, api, projectPath(p.ID, "workers"), nil, "presets")
	}
	list, err := c.fetchWorkers(ctx, api, p)
	if err != nil {
		return err
	}
	// A preset runs in the runtime it names; only those the project has are offered, as
	// in the web interface.
	rows := make([][]string, 0, len(list.Presets))
	suggested := false
	for _, pr := range list.Presets {
		runtime := pr.Runtime
		if runtime == "" {
			runtime = "php"
		}
		if !p.hasService(runtime) {
			continue
		}
		id := pr.ID
		if pr.ID == list.SuggestedPreset {
			id += " *"
			suggested = true
		}
		argument := ""
		if pr.ArgLabel != "" {
			argument = pr.ArgLabel
			if pr.ArgHint != "" {
				argument += ": " + pr.ArgHint
			}
		}
		rows = append(rows, []string{id, pr.Description, argument})
	}
	if len(rows) == 0 {
		c.printf("No preset fits %s: it has no runtime a worker can run in.\n", p.Slug)
		return nil
	}
	c.table([]string{"PRESET", "RUNS", "--ARG"}, rows)
	if suggested {
		c.printf("\n* suggested for %s\n", p.Slug)
	}
	return nil
}

func (c *cli) workerAdd(ctx context.Context, args []string) error {
	fs := c.newFlags("worker add")
	preset := fs.String("preset", "", "what the worker runs (envoryx worker presets <project>)")
	argument := fs.String("arg", "", "the preset's argument: queues, a script path …")
	disabled := fs.Bool("disabled", false, "add it without starting it")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *preset == "" {
		return usagef("usage: envoryx worker add <project> <name> --preset ID [--arg VALUE] [--disabled]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Worker workerInfo `json:"worker"`
	}
	req := map[string]any{"name": pos[1], "preset": *preset, "arg": *argument, "enabled": !*disabled}
	if err := api.post(ctx, projectPath(p.ID, "workers"), req, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Worker)
	}
	c.printf("Added the worker %s to %s: %s\n", out.Worker.Name, p.Slug, strings.Join(out.Worker.Command, " "))
	return nil
}

// workerEdit changes what was given and keeps the rest; enable/disable pass enabled.
func (c *cli) workerEdit(ctx context.Context, args []string, enabled *bool) error {
	name := "worker edit"
	if enabled != nil {
		name = "worker enable"
		if !*enabled {
			name = "worker disable"
		}
	}
	fs := c.newFlags(name)
	newName := fs.String("name", "", "rename it")
	preset := fs.String("preset", "", "run another preset")
	argument := fs.String("arg", "", "the preset's argument")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		if enabled != nil {
			return usagef("usage: envoryx %s <project> <worker>", name)
		}
		return usagef("usage: envoryx worker edit <project> <worker> [--name NAME] [--preset ID] [--arg VALUE]")
	}
	set := flagsSet(fs)
	if enabled == nil && !set["name"] && !set["preset"] && !set["arg"] {
		return usagef("nothing to change: give --name, --preset or --arg")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	w, err := c.findWorker(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	req := map[string]any{"name": w.Name, "preset": w.Preset, "arg": w.Arg, "enabled": w.Enabled}
	if set["name"] {
		req["name"] = *newName
	}
	if set["preset"] {
		req["preset"] = *preset
		// Another preset rarely takes the old one's argument.
		if !set["arg"] {
			req["arg"] = ""
		}
	}
	if set["arg"] {
		req["arg"] = *argument
	}
	if enabled != nil {
		req["enabled"] = *enabled
	}
	var out struct {
		Worker workerInfo `json:"worker"`
	}
	if err := api.do(ctx, http.MethodPut, projectPath(p.ID, "workers", url.PathEscape(w.ID)), nil, req, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Worker)
	}
	switch {
	case enabled != nil && *enabled:
		c.printf("Enabled the worker %s of %s.\n", out.Worker.Name, p.Slug)
	case enabled != nil:
		c.printf("Disabled the worker %s of %s.\n", out.Worker.Name, p.Slug)
	default:
		c.printf("Updated the worker %s of %s: %s\n", out.Worker.Name, p.Slug, strings.Join(out.Worker.Command, " "))
	}
	return nil
}

func (c *cli) workerRestart(ctx context.Context, args []string) error {
	fs := c.newFlags("worker restart")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx worker restart <project> <worker>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	w, err := c.findWorker(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	if err := api.post(ctx, projectPath(p.ID, "workers", url.PathEscape(w.ID), "restart"), nil, nil); err != nil {
		return err
	}
	if !c.json {
		c.printf("Restarted the worker %s of %s.\n", w.Name, p.Slug)
	}
	return nil
}

func (c *cli) workerRemove(ctx context.Context, args []string) error {
	fs := c.newFlags("worker remove")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx worker remove <project> <worker> --yes")
	}
	if !*yes {
		return usagef("removing stops the worker %s and deletes its definition; confirm with --yes", pos[1])
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	w, err := c.findWorker(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	if err := api.do(ctx, http.MethodDelete, projectPath(p.ID, "workers", url.PathEscape(w.ID)), nil, nil, nil); err != nil {
		return err
	}
	if !c.json {
		c.printf("Removed the worker %s from %s.\n", w.Name, p.Slug)
	}
	return nil
}

// flagsSet names the flags that were given on the command line, so an edit can tell
// --arg "" (clear it) from no --arg at all (keep it).
func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// ---- cron jobs ---------------------------------------------------------------

type cronRunInfo struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	ExitCode   int        `json:"exitCode"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Output     *string    `json:"output"`
	Truncated  bool       `json:"truncated"`
}

// duration is how long the run took, or "running".
func (r cronRunInfo) duration() string {
	if r.FinishedAt == nil {
		return "running"
	}
	return r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
}

type cronJobInfo struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Runtime        string       `json:"runtime"`
	Schedule       string       `json:"schedule"`
	Command        string       `json:"command"`
	TimeoutSeconds int          `json:"timeoutSeconds"`
	Enabled        bool         `json:"enabled"`
	NextRun        *time.Time   `json:"nextRun"`
	Running        bool         `json:"running"`
	RuntimeMissing bool         `json:"runtimeMissing"`
	LastRun        *cronRunInfo `json:"lastRun"`
}

// cronRuntimes are the services a cron job can run in.
var cronRuntimes = []string{"php", "node", "python", "go", "ruby", "java", "dotnet"}

func (c *cli) cronCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIHelp
	case "", "list", "ls":
		return c.cronList(ctx, rest)
	case "add", "create":
		return c.cronAdd(ctx, rest)
	case "edit", "update":
		return c.cronEdit(ctx, rest, nil)
	case "enable", "disable":
		enabled := cmd == "enable"
		return c.cronEdit(ctx, rest, &enabled)
	case "run":
		return c.cronRun(ctx, rest)
	case "runs", "history":
		return c.cronRuns(ctx, rest)
	case "remove", "delete", "rm":
		return c.cronRemove(ctx, rest)
	default:
		return usagef("unknown cron command %q", cmd)
	}
}

func (c *cli) findCronJob(ctx context.Context, api *client, p projectSummary, want string) (cronJobInfo, error) {
	if want == "" {
		return cronJobInfo{}, usagef("which cron job? envoryx cron list %s shows them", p.Slug)
	}
	var body struct {
		Jobs []cronJobInfo `json:"jobs"`
	}
	if err := api.get(ctx, projectPath(p.ID, "cron"), nil, &body); err != nil {
		return cronJobInfo{}, err
	}
	for _, j := range body.Jobs {
		if j.ID == want || strings.EqualFold(j.Name, want) {
			return j, nil
		}
	}
	return cronJobInfo{}, fmt.Errorf("%s has no cron job %q (envoryx cron list %s shows them)", p.Slug, want, p.Slug)
}

func (c *cli) cronList(ctx context.Context, args []string) error {
	fs := c.newFlags("cron list")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: envoryx cron list <project>")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		return c.printRaw(ctx, api, projectPath(p.ID, "cron"), nil, "jobs")
	}
	var body struct {
		Jobs     []cronJobInfo `json:"jobs"`
		Timezone string        `json:"timezone"`
	}
	if err := api.get(ctx, projectPath(p.ID, "cron"), nil, &body); err != nil {
		return err
	}
	if len(body.Jobs) == 0 {
		c.printf("%s has no cron jobs. Add one with: envoryx cron add %s <name> --schedule \"0 3 * * *\" -- <command>\n", p.Slug, p.Slug)
		return nil
	}
	rows := make([][]string, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		next := "-"
		switch {
		case !j.Enabled:
			next = "disabled"
		case j.RuntimeMissing:
			next = "no " + j.Runtime + " service"
		case j.NextRun != nil:
			next = j.NextRun.Local().Format("2006-01-02 15:04")
		}
		last := "-"
		if j.Running {
			last = "running"
		} else if r := j.LastRun; r != nil {
			last = r.Status + " " + r.StartedAt.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{j.Name, j.Schedule, j.Runtime, next, last, firstLine(j.Command)})
	}
	c.table([]string{"NAME", "SCHEDULE", "RUNTIME", "NEXT", "LAST", "COMMAND"}, rows)
	if body.Timezone != "" {
		c.printf("\nSchedules are read in %s.\n", body.Timezone)
	}
	return nil
}

// firstLine shortens a multi-line command for a table.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// cronCommandArg takes the command from --command or from everything after "--".
func cronCommandArg(flagValue string, after []string) string {
	if len(after) > 0 {
		return strings.Join(after, " ")
	}
	return flagValue
}

func (c *cli) cronAdd(ctx context.Context, args []string) error {
	own, after := splitAtDashDash(args)
	fs := c.newFlags("cron add")
	schedule := fs.String("schedule", "", `when it runs: "*/15 * * * *", "@daily" …`)
	command := fs.String("command", "", "the shell command (or give it after --)")
	runtime := fs.String("runtime", "", "the service it runs in (default: the project's application)")
	timeout := fs.Duration("max-time", 0, "stop a run after this long (default 10m, at most 24h)")
	disabled := fs.Bool("disabled", false, "add it without scheduling it")
	pos, err := parse(fs, own)
	if err != nil {
		return err
	}
	cmd := cronCommandArg(*command, after)
	if len(pos) != 2 || *schedule == "" || cmd == "" {
		return usagef(`usage: envoryx cron add <project> <name> --schedule "0 3 * * *" [--runtime php] -- <command…>`)
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	rt := *runtime
	if rt == "" {
		rt = p.appService()
		if !slices.Contains(cronRuntimes, rt) {
			return usagef("%s has no application runtime a cron job could use; give --runtime (%s)", p.Slug, strings.Join(cronRuntimes, ", "))
		}
	}
	req := map[string]any{"name": pos[1], "runtime": rt, "schedule": *schedule, "command": cmd,
		"timeoutSeconds": int(timeout.Seconds()), "enabled": !*disabled}
	var out struct {
		Job cronJobInfo `json:"job"`
	}
	if err := api.post(ctx, projectPath(p.ID, "cron"), req, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Job)
	}
	c.printf("Added the cron job %s to %s (%s)\n", out.Job.Name, p.Slug, out.Job.Schedule)
	if out.Job.NextRun != nil {
		c.printf("  next run %s\n", out.Job.NextRun.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func (c *cli) cronEdit(ctx context.Context, args []string, enabled *bool) error {
	name := "cron edit"
	if enabled != nil {
		name = "cron enable"
		if !*enabled {
			name = "cron disable"
		}
	}
	own, after := splitAtDashDash(args)
	fs := c.newFlags(name)
	newName := fs.String("name", "", "rename it")
	schedule := fs.String("schedule", "", "a new schedule")
	command := fs.String("command", "", "a new command (or give it after --)")
	runtime := fs.String("runtime", "", "run it in another service")
	timeout := fs.Duration("max-time", 0, "stop a run after this long")
	pos, err := parse(fs, own)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		if enabled != nil {
			return usagef("usage: envoryx %s <project> <job>", name)
		}
		return usagef(`usage: envoryx cron edit <project> <job> [--name NAME] [--schedule "0 3 * * *"] [--runtime NAME] [--max-time 10m] [-- <command…>]`)
	}
	set := flagsSet(fs)
	changesCommand := set["command"] || len(after) > 0
	if enabled == nil && !changesCommand && !set["name"] && !set["schedule"] && !set["runtime"] && !set["max-time"] {
		return usagef("nothing to change: give --name, --schedule, --runtime, --max-time or a command")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	j, err := c.findCronJob(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	req := map[string]any{"name": j.Name, "runtime": j.Runtime, "schedule": j.Schedule, "command": j.Command,
		"timeoutSeconds": j.TimeoutSeconds, "enabled": j.Enabled}
	if set["name"] {
		req["name"] = *newName
	}
	if set["schedule"] {
		req["schedule"] = *schedule
	}
	if changesCommand {
		req["command"] = cronCommandArg(*command, after)
	}
	if set["runtime"] {
		req["runtime"] = *runtime
	}
	if set["max-time"] {
		req["timeoutSeconds"] = int(timeout.Seconds())
	}
	if enabled != nil {
		req["enabled"] = *enabled
	}
	var out struct {
		Job cronJobInfo `json:"job"`
	}
	if err := api.do(ctx, http.MethodPut, projectPath(p.ID, "cron", url.PathEscape(j.ID)), nil, req, &out); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(out.Job)
	}
	switch {
	case enabled != nil && *enabled:
		c.printf("Enabled the cron job %s of %s.\n", out.Job.Name, p.Slug)
	case enabled != nil:
		c.printf("Disabled the cron job %s of %s.\n", out.Job.Name, p.Slug)
	default:
		c.printf("Updated the cron job %s of %s (%s).\n", out.Job.Name, p.Slug, out.Job.Schedule)
	}
	return nil
}

// cronRun starts a job now. With --wait it follows the run to its end, prints the output
// and exits with the command's exit code, which is what a script wants.
func (c *cli) cronRun(ctx context.Context, args []string) error {
	fs := c.newFlags("cron run")
	wait := fs.Bool("wait", false, "wait for the run, print its output and exit with its code")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx cron run <project> <job> [--wait]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	j, err := c.findCronJob(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	var started struct {
		Run cronRunInfo `json:"run"`
	}
	if err := api.post(ctx, projectPath(p.ID, "cron", url.PathEscape(j.ID), "run"), nil, &started); err != nil {
		return err
	}
	if !*wait {
		if c.json {
			return c.printJSON(started.Run)
		}
		c.printf("Started the cron job %s of %s (run %s). envoryx cron runs %s %s shows how it went.\n", j.Name, p.Slug, started.Run.ID, p.Slug, j.Name)
		return nil
	}
	run, err := c.waitForCronRun(ctx, api, p, j, started.Run.ID)
	if err != nil {
		return err
	}
	if c.json {
		if err := c.printJSON(run); err != nil {
			return err
		}
	} else {
		if run.Output != nil && *run.Output != "" {
			c.printf("%s", *run.Output)
			if !strings.HasSuffix(*run.Output, "\n") {
				c.printf("\n")
			}
		}
		if run.Truncated {
			fmt.Fprintln(c.errOut, "envoryx: the output was cut short; only its end is kept")
		}
	}
	switch run.Status {
	case "succeeded":
		return nil
	case "failed":
		return &exitCodeError{code: run.ExitCode}
	default:
		return fmt.Errorf("the run ended %s", strings.ReplaceAll(run.Status, "_", " "))
	}
}

// cronPollInterval is how often "cron run --wait" asks how the run is going.
var cronPollInterval = time.Second

func (c *cli) waitForCronRun(ctx context.Context, api *client, p projectSummary, j cronJobInfo, runID string) (cronRunInfo, error) {
	for {
		var body struct {
			Runs []cronRunInfo `json:"runs"`
		}
		if err := api.get(ctx, projectPath(p.ID, "cron", url.PathEscape(j.ID), "runs"), nil, &body); err != nil {
			return cronRunInfo{}, err
		}
		for _, r := range body.Runs {
			if r.ID == runID && r.Status != "running" {
				return r, nil
			}
		}
		select {
		case <-ctx.Done():
			return cronRunInfo{}, ctx.Err()
		case <-time.After(cronPollInterval):
		}
	}
}

func (c *cli) cronRuns(ctx context.Context, args []string) error {
	fs := c.newFlags("cron runs")
	output := fs.Bool("output", false, "print the newest run's output instead of the table")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx cron runs <project> <job> [--output]")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	j, err := c.findCronJob(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	path := projectPath(p.ID, "cron", url.PathEscape(j.ID), "runs")
	if c.json && !*output {
		return c.printRaw(ctx, api, path, nil, "runs")
	}
	var body struct {
		Runs []cronRunInfo `json:"runs"`
	}
	if err := api.get(ctx, path, nil, &body); err != nil {
		return err
	}
	if len(body.Runs) == 0 {
		c.printf("The cron job %s of %s has not run yet.\n", j.Name, p.Slug)
		return nil
	}
	if *output {
		r := body.Runs[0]
		if c.json {
			return c.printJSON(r)
		}
		if r.Output != nil {
			c.printf("%s", *r.Output)
		}
		return nil
	}
	rows := make([][]string, 0, len(body.Runs))
	for _, r := range body.Runs {
		exit := ""
		if r.FinishedAt != nil && r.Status != "error" && r.Status != "interrupted" {
			exit = strconv.Itoa(r.ExitCode)
		}
		rows = append(rows, []string{r.StartedAt.Local().Format("2006-01-02 15:04:05"), r.Source, r.Status, exit, r.duration()})
	}
	c.table([]string{"STARTED", "SOURCE", "STATUS", "EXIT", "TOOK"}, rows)
	return nil
}

func (c *cli) cronRemove(ctx context.Context, args []string) error {
	fs := c.newFlags("cron remove")
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: envoryx cron remove <project> <job> --yes")
	}
	if !*yes {
		return usagef("removing deletes the cron job %s and its run history; confirm with --yes", pos[1])
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, api, err := c.projectAndClient(ctx, pos[0])
	if err != nil {
		return err
	}
	j, err := c.findCronJob(ctx, api, p, pos[1])
	if err != nil {
		return err
	}
	if err := api.do(ctx, http.MethodDelete, projectPath(p.ID, "cron", url.PathEscape(j.ID)), nil, nil, nil); err != nil {
		return err
	}
	if !c.json {
		c.printf("Removed the cron job %s from %s.\n", j.Name, p.Slug)
	}
	return nil
}

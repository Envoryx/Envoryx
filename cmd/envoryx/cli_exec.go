// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/coder/websocket"
	"golang.org/x/term"
)

// maxExecStdin is what the CLI is willing to push through the API's 1 MiB request body.
// Bigger input belongs in SSH (ssh <slug>@host "…" < file), which streams.
const maxExecStdin = 512 << 10

// projectExec runs one command in a project container and hands its exit code back to
// the calling shell. stdout and stderr arrive separately, so pipelines and CI jobs see
// what they expect; the interactive counterpart is the terminal in the web interface or
// plain SSH.
func (c *cli) projectExec(ctx context.Context, args []string) error {
	// Everything after "--" is the command, never a flag of ours.
	own, command := splitAtDashDash(args)
	fs := c.newFlags("project exec")
	service := fs.String("service", "", "service to run in (default: the application container)")
	noStdin := fs.Bool("no-stdin", false, "do not pass stdin on, even when it is piped")
	pos, err := parse(fs, own)
	if err != nil {
		return err
	}
	if len(command) == 0 {
		// "exec shop ls -la" without the separator still reads unambiguously as long as
		// the command carries no flags of its own; the separator is what makes it safe.
		if len(pos) > 1 {
			command, pos = pos[1:], pos[:1]
		} else {
			return usagef("which command? envoryx project exec <project> -- <command> [args…]")
		}
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	kind := *service
	if kind == "" {
		kind = p.appService()
	}
	if err := c.requireService(p, kind); err != nil {
		return err
	}
	body := map[string]any{"cmd": command}
	if !*noStdin {
		stdin, err := c.pipedStdin(p.Slug)
		if err != nil {
			return err
		}
		if stdin != "" {
			body["stdin"] = stdin
		}
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	resp, err := api.request(ctx, http.MethodPost, projectPath(p.ID, "services", kind, "exec"), nil, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The answer is one JSON object per line: output while it runs, then the exit code.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	exit := 0
	for scanner.Scan() {
		var frame struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "stdout":
			fmt.Fprint(c.out, frame.Text)
		case "stderr":
			fmt.Fprint(c.errOut, frame.Text)
		case "exit":
			exit = frame.Code
		case "error":
			return errors.New(frame.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading the command's output: %w", err)
	}
	if exit != 0 {
		return &exitCodeError{code: exit}
	}
	return nil
}

// splitAtDashDash separates our own arguments from the command to run.
func splitAtDashDash(args []string) (own, command []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// pipedStdin reads input that was piped or redirected into the CLI. A terminal is left
// alone – nobody wants a command to block on input nobody typed.
func (c *cli) pipedStdin(slug string) (string, error) {
	f, ok := c.stdin.(*os.File)
	if ok && term.IsTerminal(int(f.Fd())) {
		return "", nil
	}
	if ok {
		info, err := f.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(c.stdin, maxExecStdin+1))
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	if len(data) > maxExecStdin {
		return "", fmt.Errorf("stdin is larger than %d KiB, which the API's request body cannot hold; pipe it through SSH instead: ssh %s@<host> -p 2222 \"…\" < file", maxExecStdin>>10, slug)
	}
	return string(data), nil
}

// ---- actions -----------------------------------------------------------------

type actionInfo struct {
	ID          string   `json:"id"`
	Group       string   `json:"group"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Service     string   `json:"service"`
	Cmd         []string `json:"cmd"`
	Destructive bool     `json:"destructive"`
	Available   bool     `json:"available"`
	Reason      string   `json:"reason"`
}

// projectRun runs one of the catalogue actions (composer install, artisan migrate, npm
// ci …) and streams its output. Without an action it lists what this project offers.
func (c *cli) projectRun(ctx context.Context, args []string) error {
	fs := c.newFlags("project run")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	action := arg(pos, 1)
	if action == "" {
		if c.json {
			return c.printRaw(ctx, api, projectPath(p.ID, "actions"), nil, "actions")
		}
		var body struct {
			Actions []actionInfo `json:"actions"`
		}
		if err := api.get(ctx, projectPath(p.ID, "actions"), nil, &body); err != nil {
			return err
		}
		rows := make([][]string, 0, len(body.Actions))
		for _, a := range body.Actions {
			state := "ready"
			if !a.Available {
				state = a.Reason
				if state == "" {
					state = "unavailable"
				}
			}
			rows = append(rows, []string{a.ID, strings.Join(a.Cmd, " "), state})
		}
		c.table([]string{"ACTION", "COMMAND", "STATE"}, rows)
		return nil
	}

	// Actions run over a WebSocket: the project lock is held for as long as the socket
	// is open, so closing it (Ctrl+C) also stops the command.
	conn, err := api.dialWS(ctx, projectPath(p.ID, "actions", action, "ws"), nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	exit := 0
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				break
			}
			if errors.Is(err, context.Canceled) {
				return err
			}
			return err
		}
		if typ == websocket.MessageBinary {
			// Action output comes from a pseudo-terminal, carriage returns and all.
			fmt.Fprint(c.out, string(data))
			continue
		}
		var msg struct {
			Type    string   `json:"type"`
			Cmd     []string `json:"cmd"`
			Code    int      `json:"code"`
			Message string   `json:"message"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "start":
			if !c.json {
				fmt.Fprintf(c.errOut, "› %s\n", strings.Join(msg.Cmd, " "))
			}
		case "exit":
			exit = msg.Code
		case "error":
			return errors.New(msg.Message)
		}
	}
	if exit != 0 {
		return &exitCodeError{code: exit}
	}
	return nil
}

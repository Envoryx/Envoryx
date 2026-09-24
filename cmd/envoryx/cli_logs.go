// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type logLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

// projectLogs prints a service's recent output and, with --follow, keeps printing until
// Ctrl+C. stderr of the container goes to stderr here, so "envoryx project logs … >
// file" keeps the two apart the way a shell pipeline expects.
func (c *cli) projectLogs(ctx context.Context, args []string) error {
	fs := c.newFlags("project logs")
	service := fs.String("service", "", "service: php, node, python, web, database, redis, memcached, mailpit, rabbitmq, meilisearch, typesense, opensearch, opensearch-dashboards, storage or worker:<id>")
	tail := fs.Int("tail", 200, "how many lines of history")
	follow := fs.Bool("follow", false, "keep reading")
	fs.BoolVar(follow, "f", false, "keep reading")
	timestamps := fs.Bool("timestamps", false, "prefix every line with its time")
	since := fs.String("since", "", "only lines from this time on: RFC 3339 or a duration back from now (30m, 6h, 7d)")
	until := fs.String("until", "", "only lines up to this time (not with --follow)")
	grep := fs.String("grep", "", "only lines containing this text (case-insensitive)")
	level := fs.String("level", "", "only warnings and errors (warn) or only errors (error), as guessed from the text")
	output := fs.String("o", "", "write every matching line to this file instead of the last --tail (\"-\" is stdout)")
	fs.StringVar(output, "output", "", "write every matching line to this file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *follow && (*until != "" || *output != "") {
		return errors.New("--until and -o cannot be combined with --follow")
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
	api, err := c.connect()
	if err != nil {
		return err
	}
	query := url.Values{"tail": {strconv.Itoa(*tail)}}
	for k, v := range map[string]string{"since": *since, "until": *until, "q": *grep, "level": *level} {
		if v != "" {
			query.Set(k, v)
		}
	}
	if *output != "" {
		query.Del("tail")
		return c.exportLogs(ctx, api, p, kind, query, *output)
	}
	if !*follow {
		if c.json {
			return c.printRaw(ctx, api, projectPath(p.ID, "services", kind, "logs"), query, "lines")
		}
		var body struct {
			Lines []logLine `json:"lines"`
		}
		if err := api.get(ctx, projectPath(p.ID, "services", kind, "logs"), query, &body); err != nil {
			return err
		}
		for _, l := range body.Lines {
			c.writeLogLine(l, *timestamps)
		}
		return nil
	}

	conn, err := api.dialWS(ctx, projectPath(p.ID, "services", kind, "logs", "ws"), query)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Ctrl+C and the server closing the stream at the end are both normal.
			if errors.Is(err, context.Canceled) || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		var msg struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			logLine
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "line":
			if c.json {
				fmt.Fprintln(c.out, string(data))
				continue
			}
			c.writeLogLine(msg.logLine, *timestamps)
		case "error":
			return errors.New(msg.Message)
		}
	}
}

// exportLogs saves the download endpoint's file: all matching lines, not just a tail.
func (c *cli) exportLogs(ctx context.Context, api *client, p projectSummary, kind string, query url.Values, target string) error {
	resp, err := api.request(ctx, http.MethodGet, projectPath(p.ID, "services", kind, "logs", "download"), query, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if target == "-" {
		_, err := io.Copy(c.out, resp.Body)
		return err
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	written, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	c.printf("Wrote %s (%s)\n", target, humanSize(written))
	return nil
}

func (c *cli) writeLogLine(l logLine, timestamps bool) {
	w := c.out
	if l.Stream == "stderr" {
		w = c.errOut
	}
	text := strings.TrimRight(l.Text, "\r\n")
	if timestamps && !l.Time.IsZero() {
		fmt.Fprintf(w, "%s  %s\n", l.Time.Local().Format(time.RFC3339), text)
		return
	}
	fmt.Fprintln(w, text)
}

func readAllLimited(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r, 1<<20))
}

// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net/http"
	"time"
)

type shareInfo struct {
	Active    bool      `json:"active"`
	State     string    `json:"state"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	Message   string    `json:"message"`
}

// projectShare puts a project on a temporary public address (Cloudflare quick tunnel),
// shows the current one (--status) or ends it (--stop).
func (c *cli) projectShare(ctx context.Context, args []string) error {
	fs := c.newFlags("project share")
	duration := fs.Duration("for", time.Hour, "how long the share lasts (5m to 24h)")
	stop := fs.Bool("stop", false, "end the share")
	status := fs.Bool("status", false, "show the share without changing it")
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
	path := projectPath(p.ID, "share")
	var body struct {
		Share shareInfo `json:"share"`
	}
	switch {
	case *stop:
		if err := api.do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		c.printf("%s is no longer shared.\n", p.Slug)
		return nil
	case *status:
		if err := api.get(ctx, path, nil, &body); err != nil {
			return err
		}
	default:
		if err := api.post(ctx, path, map[string]any{"minutes": int(duration.Minutes())}, &body); err != nil {
			return err
		}
	}
	if c.json {
		return c.printJSON(body.Share)
	}
	s := body.Share
	switch {
	case !s.Active:
		c.printf("%s is not shared.\n", p.Slug)
	case s.URL != "":
		c.printf("%s\n  public until %s – anyone with the address can open the project\n", s.URL, s.ExpiresAt.Local().Format("2006-01-02 15:04"))
	default:
		c.printf("The tunnel of %s is %s. %s\n", p.Slug, s.State, s.Message)
	}
	return nil
}

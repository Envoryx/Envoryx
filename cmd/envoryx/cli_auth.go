// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// meResponse is what GET /api/v1/auth/me says about the caller.
type meResponse struct {
	User struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
	Token *struct {
		Name     string   `json:"name"`
		Scope    string   `json:"scope"`
		Projects []string `json:"projects"`
	} `json:"token"`
}

// login stores the server address and an API token so later commands need no flags. The
// token is checked against the server before anything is written, so a typo is noticed
// here and not in the middle of a script.
func (c *cli) login(ctx context.Context, args []string) error {
	fs := c.newFlags("login")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if c.flags.Token == "" && os.Getenv("ENVORYX_TOKEN") == "" {
		token, err := c.readToken()
		if err != nil {
			return err
		}
		c.flags.Token = token
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	var me meResponse
	if err := api.get(ctx, "/api/v1/auth/me", nil, &me); err != nil {
		return err
	}
	if err := saveCLIConfig(c.cfg); err != nil {
		return fmt.Errorf("saving %s: %w", cliConfigPath(), err)
	}
	if c.json {
		return c.printJSON(me)
	}
	c.printf("Signed in to %s as %s.\n", c.cfg.URL, me.User.Username)
	c.printf("%s\nSaved to %s\n", describeToken(me), cliConfigPath())
	return nil
}

// readToken asks for the token without echoing it; a token piped in on stdin is read as
// it comes, which is what a provisioning script does.
func (c *cli) readToken() (string, error) {
	if f, ok := c.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(c.errOut, "API token (web interface → Settings → API tokens): ")
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.errOut)
		if err != nil {
			return "", fmt.Errorf("reading the token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "-", nil // resolveConfig reads stdin
}

// logout forgets the stored token. The token itself stays valid – it is revoked in the
// web interface under Settings → API tokens.
func (c *cli) logout(args []string) error {
	fs := c.newFlags("logout")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	path := cliConfigPath()
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.printf("Nothing stored (%s).\n", path)
			return nil
		}
		return err
	}
	c.printf("Removed %s. The token itself stays valid until it is revoked under Settings → API tokens.\n", path)
	return nil
}

// whoami reports which account the token belongs to and what it is allowed to do.
func (c *cli) whoami(ctx context.Context, args []string) error {
	fs := c.newFlags("whoami")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	var me meResponse
	if err := api.get(ctx, "/api/v1/auth/me", nil, &me); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(me)
	}
	c.printf("%s at %s\n%s\n", me.User.Username, c.cfg.URL, describeToken(me))
	return nil
}

func describeToken(me meResponse) string {
	if me.Token == nil {
		return "Browser session – every operation is allowed."
	}
	s := fmt.Sprintf("Token %q, scope %s", me.Token.Name, me.Token.Scope)
	switch len(me.Token.Projects) {
	case 0:
		return s + " (all projects)"
	case 1:
		return s + " (one project only)"
	default:
		return fmt.Sprintf("%s (%d projects only)", s, len(me.Token.Projects))
	}
}

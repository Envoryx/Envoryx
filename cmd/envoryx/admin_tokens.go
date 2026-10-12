// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

// API tokens can't manage tokens over the API (a leaked one must not mint its own
// successors), so a script that needs one, such as a CI setup, creates it here, from a
// shell in the container, which has every power anyway.

type tokenRequest struct {
	name, user, scope string
	projects          []string
	quiet             bool
}

func (c *adminCLI) tokens(ctx context.Context, username string) error {
	tokens, err := c.store.Tokens.List(ctx)
	if err != nil {
		return err
	}
	users, err := c.userNames(ctx)
	if err != nil {
		return err
	}
	projects, err := c.projectSlugs(ctx)
	if err != nil {
		return err
	}
	if username != "" {
		u, err := c.pickUser(ctx, username)
		if err != nil {
			return err
		}
		kept := tokens[:0]
		for _, t := range tokens {
			if t.UserID == u.ID {
				kept = append(kept, t)
			}
		}
		tokens = kept
	}
	if len(tokens) == 0 {
		fmt.Fprintln(c.out, "No API tokens. envoryx admin create-token --name NAME creates one.")
		return nil
	}
	tw := tabwriter.NewWriter(c.out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tOWNER\tSCOPE\tPROJECTS\tPREFIX\tCREATED\tLAST USED (UTC)\tID")
	for _, t := range tokens {
		scoped := "all"
		if len(t.ProjectIDs) > 0 {
			names := make([]string, len(t.ProjectIDs))
			for i, id := range t.ProjectIDs {
				names[i] = projects[id]
				if names[i] == "" {
					names[i] = id
				}
			}
			scoped = strings.Join(names, ",")
		}
		used := "never"
		if t.LastUsedAt != nil {
			used = t.LastUsedAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s…\t%s\t%s\t%s\n", t.Name, users[t.UserID], t.Scope, scoped, t.Prefix, t.CreatedAt.Format("2006-01-02"), used, t.ID)
	}
	return tw.Flush()
}

func (c *adminCLI) createToken(ctx context.Context, req tokenRequest) error {
	user, err := c.pickUser(ctx, req.user)
	if err != nil {
		return err
	}
	if user.Disabled {
		return fmt.Errorf("the account %s is disabled", user.Username)
	}
	if user.Support {
		return errors.New("the hoster's support gets no API tokens: its access ends with the admins' consent")
	}
	scope, err := auth.ParseScope(req.scope)
	if err != nil {
		return fmt.Errorf("--scope must be read, operate or admin, not %q", req.scope)
	}
	svc := auth.NewService(c.store, auth.Options{}, slog.New(slog.DiscardHandler))
	p, err := svc.PrincipalFor(ctx, user)
	if err != nil {
		return err
	}
	if max := p.MaxScope(); !max.Covers(scope) {
		if max == "" {
			return fmt.Errorf("the role of %s allows no API tokens", user.Username)
		}
		return fmt.Errorf("the role of %s allows at most %s tokens", user.Username, max)
	}
	ids := make([]string, 0, len(req.projects))
	for _, want := range req.projects {
		proj, err := c.findProject(ctx, want)
		if err != nil {
			return err
		}
		if !p.CanAccessProject(proj.ID) {
			return fmt.Errorf("%s has no access to the project %s", user.Username, proj.Slug)
		}
		ids = append(ids, proj.ID)
	}
	secret, t, err := svc.CreateAPIToken(ctx, p, auth.TokenSpec{Name: req.name, Scope: scope, Projects: ids})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidTokenName) {
			return errors.New("the name must be 1-64 characters without control characters")
		}
		return err
	}
	c.audit.LogAs(ctx, "cli", "token.created", "token", t.ID, map[string]any{"name": t.Name, "scope": t.Scope, "projects": t.ProjectIDs, "owner": user.Username})
	if req.quiet {
		fmt.Fprintln(c.out, secret)
		return nil
	}
	fmt.Fprintf(c.out, "API token %q for %s (%s scope), shown once:\n\n  %s\n\n", t.Name, user.Username, t.Scope, secret)
	fmt.Fprintln(c.out, "Use it with envoryx login --token -, or as ENVORYX_TOKEN.")
	return nil
}

func (c *adminCLI) revokeToken(ctx context.Context, want, username string) error {
	tokens, err := c.store.Tokens.List(ctx)
	if err != nil {
		return err
	}
	owner := ""
	if username != "" {
		u, err := c.pickUser(ctx, username)
		if err != nil {
			return err
		}
		owner = u.ID
	}
	var matches []store.APIToken
	for _, t := range tokens {
		if owner != "" && t.UserID != owner {
			continue
		}
		// A whole secret pasted by mistake still finds its token by the prefix.
		if t.ID == want || t.Name == want || strings.HasPrefix(want, t.Prefix) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("no API token %q (envoryx admin tokens lists them)", want)
	case 1:
	default:
		return fmt.Errorf("%q matches %d tokens; name it by its id, or add --user", want, len(matches))
	}
	t := matches[0]
	if err := c.store.Tokens.Delete(ctx, t.ID); err != nil {
		return err
	}
	c.audit.LogAs(ctx, "cli", "token.revoked", "token", t.ID, map[string]any{"name": t.Name})
	fmt.Fprintf(c.out, "API token %q revoked.\n", t.Name)
	return nil
}

func (c *adminCLI) userNames(ctx context.Context) (map[string]string, error) {
	users, err := c.store.Users.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Username
	}
	return names, nil
}

func (c *adminCLI) projectSlugs(ctx context.Context) (map[string]string, error) {
	projects, err := c.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	slugs := make(map[string]string, len(projects))
	for _, p := range projects {
		slugs[p.ID] = p.Slug
	}
	return slugs, nil
}

// findProject resolves a project by its id, its slug or its name, like the API client.
func (c *adminCLI) findProject(ctx context.Context, want string) (store.Project, error) {
	projects, err := c.store.Projects.List(ctx)
	if err != nil {
		return store.Project{}, err
	}
	var matches []store.Project
	for _, p := range projects {
		if p.ID == want || p.Slug == want || strings.EqualFold(p.Name, want) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return store.Project{}, fmt.Errorf("no project %q", want)
	default:
		return store.Project{}, fmt.Errorf("%q matches several projects; use the slug or the id", want)
	}
}

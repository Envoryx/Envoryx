// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// execEnv is how a command runs inside a service container: application containers (php,
// python, node) run as the project owner in the project directory, every other service as
// the image's own user at the root, the way the terminal has always opened them.
type execEnv struct {
	User       string
	WorkingDir string
	Env        []string
}

func (m *Manager) execEnv(kind store.ServiceKind) (execEnv, error) {
	paths, err := m.paths()
	if err != nil {
		return execEnv{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	e := execEnv{WorkingDir: "/", Env: []string{"LANG=C.UTF-8"}}
	switch kind {
	case store.ServicePHP, store.ServicePython, store.ServiceGo, store.ServiceRuby, store.ServiceNode:
		e.WorkingDir = appMountTarget
		e.User = fmt.Sprintf("%d:%d", paths.PUID, paths.PGID)
		// The uid usually has no passwd entry in the image; give tools writable caches.
		e.Env = append(e.Env, toolEnv...)
		switch kind {
		case store.ServicePython:
			e.Env = append(e.Env, pythonEnv...)
		case store.ServiceGo:
			e.Env = append(e.Env, goEnv...)
		case store.ServiceRuby:
			e.Env = append(e.Env, rubyEnv...)
		}
	}
	return e, nil
}

// ExecOptions describe one command run in a service container.
type ExecOptions struct {
	// Cmd is the command in argv form; it is never handed to a shell. Callers that want
	// pipes or redirection ask for a shell themselves ("sh", "-lc", "…").
	Cmd    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs one command in a service container and returns its exit code. It is the
// non-interactive counterpart of OpenTerminal: without a pseudo-terminal stdout and
// stderr stay apart and the exit code survives, which is what scripts, CI jobs and the
// CLI need. The project lock is deliberately not taken – a command runs alongside
// whatever else the project is doing, exactly like a terminal session does.
func (m *Manager) Exec(ctx context.Context, id string, kind store.ServiceKind, opts ExecOptions) (int, error) {
	if len(opts.Cmd) == 0 {
		return 0, fmt.Errorf("%w: command required", validate.ErrInvalid)
	}
	c, err := m.ServiceContainer(ctx, id, kind)
	if err != nil {
		return 0, err
	}
	if c.State != "running" {
		return 0, fmt.Errorf("%w: the %s container is not running", ErrConflict, kind)
	}
	env, err := m.execEnv(kind)
	if err != nil {
		return 0, err
	}
	m.ensurePasswdEntry(ctx, c.ID, c.Name, env.User)
	// CI=1 keeps composer, npm and friends from asking questions nobody can answer here;
	// the catalogue actions run with the same marker.
	m.audit.Log(ctx, audit.ActionExec, "project", id, map[string]any{
		"service": string(kind),
		"command": truncateCmd(strings.Join(opts.Cmd, " "), 200),
	})
	return m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{
		Cmd:        opts.Cmd,
		Env:        append(env.Env, "CI=1"),
		User:       env.User,
		WorkingDir: env.WorkingDir,
		Stdin:      opts.Stdin,
		Stdout:     opts.Stdout,
		Stderr:     opts.Stderr,
	})
}

func truncateCmd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

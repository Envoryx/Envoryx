package project

import (
	"context"
	"fmt"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
)

// shellCmd starts bash when the image has it and falls back to sh. The argument is a
// constant; nothing from the request is ever interpolated into it.
var shellCmd = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

// OpenTerminal opens an interactive shell in a project service container. Application
// containers (php, python, node) run the shell as the configured PUID/PGID so files created from
// the terminal belong to the project owner; the database container runs its tools as root.
func (m *Manager) OpenTerminal(ctx context.Context, id string, kind store.ServiceKind, cols, rows uint) (docker.Terminal, error) {
	c, err := m.ServiceContainer(ctx, id, kind)
	if err != nil {
		return nil, err
	}
	if c.State != "running" {
		return nil, fmt.Errorf("%w: the %s container is not running", ErrConflict, kind)
	}
	env, err := m.execEnv(kind)
	if err != nil {
		return nil, err
	}
	if cols == 0 || cols > 500 {
		cols = 120
	}
	if rows == 0 || rows > 200 {
		rows = 40
	}
	opts := docker.TerminalOptions{
		Cmd:        shellCmd,
		Cols:       cols,
		Rows:       rows,
		User:       env.User,
		WorkingDir: env.WorkingDir,
		Env:        append([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}, env.Env...),
	}
	if env.User != "" {
		opts.Env = append(opts.Env, "PS1=\\w $ ")
	}
	term, err := m.engine.OpenTerminal(ctx, c.ID, opts)
	if err != nil {
		return nil, err
	}
	m.audit.Log(ctx, audit.ActionTerminalOpened, "project", id, map[string]any{"service": string(kind)})
	return term, nil
}

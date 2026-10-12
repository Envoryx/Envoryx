// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// VsdbgAction is the catalogue action that installs Microsoft's vsdbg for VS Code.
const VsdbgAction = "dotnet:vsdbg"

// vsdbgDir is where vsdbg goes in the container: the project home, which survives a new
// image, and the path the launch.json in the IDE section names.
var vsdbgDir = path.Join(homeMountTarget, ".vsdbg")

// VsdbgVersion is the version of vsdbg in the project home ("" when it isn't installed).
// Microsoft's install script writes it to success.txt once the download is complete.
func (m *Manager) VsdbgVersion(ctx context.Context, id string) (string, error) {
	if err := validate.UUID(id); err != nil {
		return "", ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return "", err
	}
	if svc := p.Service(store.ServiceDotnet); svc == nil || !svc.Enabled {
		return "", nil
	}
	planner, err := m.planner()
	if err != nil {
		return "", err
	}
	b, err := readProjectFile(planner.HomeDir(p), ".vsdbg/success.txt", 256)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

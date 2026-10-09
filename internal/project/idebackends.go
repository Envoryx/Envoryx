package project

import (
	"context"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
)

// SettingSharedIDEBackends is the settings key of the opt-in that shares the JetBrains
// backends downloaded for Gateway between projects.
const SettingSharedIDEBackends = "shared_ide_backends"

// SharedIDEBackends reports whether projects with Gateway share one JetBrains backend
// cache. Off by default: each project keeps its own, since a shared one is writable from
// every project and a developer of one project could change what another runs.
func (m *Manager) SharedIDEBackends(ctx context.Context) bool {
	v, err := m.store.Settings.Get(ctx, SettingSharedIDEBackends)
	return err == nil && v == "true"
}

// SetSharedIDEBackends stores the preference. Projects with Gateway pick it up when their
// containers are recreated.
func (m *Manager) SetSharedIDEBackends(ctx context.Context, on bool) error {
	if err := m.store.Settings.Set(ctx, SettingSharedIDEBackends, strconv.FormatBool(on)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"sharedIdeBackends": on})
	return nil
}

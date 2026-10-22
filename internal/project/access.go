package project

import (
	"context"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// SettingNewProjectsPrivate is the settings-table key for whether a new project starts
// behind the Envoryx sign-in (store.ProxyRules.Login).
const SettingNewProjectsPrivate = "new_projects_private"

// NewProjectsPrivate reports whether new projects start behind the Envoryx sign-in. Until
// an admin decides, that is so on an instance a hoster manages, whose projects are on
// the internet, and not on a machine of one's own.
func (m *Manager) NewProjectsPrivate(ctx context.Context) bool {
	if v, err := m.store.Settings.Get(ctx, SettingNewProjectsPrivate); err == nil && v != "" {
		on, _ := strconv.ParseBool(v)
		return on
	}
	return m.Managed()
}

// SetNewProjectsPrivate stores the choice.
func (m *Manager) SetNewProjectsPrivate(ctx context.Context, on bool) error {
	changed, err := m.changeSetting(ctx, SettingNewProjectsPrivate, strconv.FormatBool(on))
	if err != nil {
		return err
	}
	if changed {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"newProjectsPrivate": on})
	}
	return nil
}

// Managed reports whether a hoster manages the instance (a plan or a fleet manager).
func (m *Manager) Managed() bool { return m.cfg.Managed != nil && m.cfg.Managed() }

// signsIn reports whether a project's host names are behind the Envoryx sign-in: its own
// choice, or the plan's when it leaves out projects open to everyone.
func (m *Manager) signsIn(p store.Project) bool {
	return p.ProxyRules.Login || !m.Plan().Allows(plan.FeaturePublicProjects)
}

package project

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/validate"
)

// SettingFolderViewFolder is the settings key of the FolderView3 folder (Unraid plugin)
// the project containers are labelled for.
const SettingFolderViewFolder = "folderview_folder"

// maxFolderViewFolder bounds the folder name; FolderView3 itself sets no limit.
const maxFolderViewFolder = 64

// FolderViewFolder returns the FolderView3 folder name, "" when containers get no folder.
func (m *Manager) FolderViewFolder(ctx context.Context) string {
	v, err := m.store.Settings.Get(ctx, SettingFolderViewFolder)
	if err != nil {
		return ""
	}
	return v
}

// SetFolderViewFolder stores the folder name ("" clears it). Containers pick it up the
// next time their project starts or is applied.
func (m *Manager) SetFolderViewFolder(ctx context.Context, folder string) error {
	folder = strings.TrimSpace(folder)
	if !utf8.ValidString(folder) || utf8.RuneCountInString(folder) > maxFolderViewFolder || strings.IndexFunc(folder, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: folder name must be at most %d characters without control characters", validate.ErrInvalid, maxFolderViewFolder)
	}
	if err := m.store.Settings.Set(ctx, SettingFolderViewFolder, folder); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"folderViewFolder": folder})
	return nil
}

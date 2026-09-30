package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A database whose server cannot start on the Docker host (MongoDB 8.0 on Linux 6.19)
// cannot be dumped, yet it is exactly the one that needs a backup before its upgrade.
// Its data volume is copied file by file instead, with the tar the addon volumes use.

// copyDatabaseVolume writes the data volume of a database as a gzipped tar to target and
// returns its size. The container is stopped first so the files hold still; it is not
// started again, since its server cannot run here anyway.
func (m *Manager) copyDatabaseVolume(ctx context.Context, p store.Project, svc *store.ProjectService, target string) (int64, error) {
	if err := m.engine.EnsureImage(ctx, addonVolumeTool, m.pullProgress(ctx, p.Slug, addonVolumeTool)); err != nil {
		return 0, err
	}
	if _, err := m.stopServiceContainer(ctx, p, svc.Kind); err != nil {
		return 0, err
	}
	if err := m.archiveVolume(ctx, p, VolumeName(p.Slug, svc.Kind), target); err != nil {
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// restoreDatabaseVolume puts a copied data volume back. The database must still be able
// to open those files: the version they were copied from, or one that takes that
// version's data over in place (the 8.0 copy an upgrade to MongoDB 8.2 took).
func (m *Manager) restoreDatabaseVolume(ctx context.Context, p store.Project, svc *store.ProjectService, copied VolumeCopyMeta, source string) error {
	if copied.Version != svc.Version && (runtime.CompareVersions(svc.Version, copied.Version) < 0 || !m.catalog.UpgradesInPlace(svc.Variant, copied.Version, svc.Version)) {
		return fmt.Errorf("%w: the backup holds a copy of %s %s data, which %s %s cannot open", validate.ErrInvalid, copied.Type, copied.Version, svc.Variant, svc.Version)
	}
	if err := m.engine.EnsureImage(ctx, addonVolumeTool, m.pullProgress(ctx, p.Slug, addonVolumeTool)); err != nil {
		return err
	}
	restart, err := m.stopServiceContainer(ctx, p, svc.Kind)
	if err != nil {
		return err
	}
	defer restart()
	volume := VolumeName(p.Slug, svc.Kind)
	if err := m.engine.CreateVolume(ctx, volume, docker.ManagedLabels(p.ID, p.Slug, "", "")); err != nil && !errors.Is(err, store.ErrConflict) && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return m.unarchiveVolume(ctx, p, volume, source)
}

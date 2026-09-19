package project

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// UnusedImage is a catalogue image that no container references any more.
type UnusedImage struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
	Size int64    `json:"size"`
}

// PruneResult reports what an image clean-up removed.
type PruneResult struct {
	Removed        []UnusedImage `json:"removed"`
	ReclaimedBytes int64         `json:"reclaimedBytes"`
	Errors         []string      `json:"errors"`
}

// catalogueRepos returns the image repositories Envoryx itself pulls (without tags).
func (m *Manager) catalogueRepos() map[string]bool {
	repos := map[string]bool{imageRepo(DBToolImage): true}
	for _, r := range m.catalog.All() {
		for _, v := range r.Versions {
			repos[imageRepo(v.Image)] = true
		}
	}
	return repos
}

func imageRepo(ref string) string {
	// Strip the tag (last ":" after the last "/") and any digest.
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		ref = ref[:colon]
	}
	return ref
}

// UnusedImages lists local images that come from the Envoryx catalogue and are not used by
// any container on the host (Envoryx's or anyone else's). Images from other sources are
// never reported, so nothing foreign can be removed through Envoryx.
func (m *Manager) UnusedImages(ctx context.Context) ([]UnusedImage, error) {
	images, err := m.engine.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := m.engine.ListContainers(ctx, false, "")
	if err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	for _, c := range containers {
		inUse[c.ImageID] = true
		inUse[c.Image] = true
	}
	// Images a project can still roll back to (or return to from a rollback) are kept.
	history, err := m.store.Images.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range history {
		if h.PreviousID != "" {
			inUse[h.PreviousID] = true
		}
		if h.Pinned {
			inUse[h.CurrentID] = true
		}
	}
	repos := m.catalogueRepos()
	var out []UnusedImage
	for _, img := range images {
		if inUse[img.ID] || len(img.Tags) == 0 {
			continue
		}
		ours, used := false, false
		for _, t := range img.Tags {
			if repos[imageRepo(t)] {
				ours = true
			}
			if inUse[t] {
				used = true
			}
		}
		if !ours || used {
			continue
		}
		out = append(out, UnusedImage{ID: img.ID, Tags: img.Tags, Size: img.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tags[0] < out[j].Tags[0] })
	if out == nil {
		out = []UnusedImage{}
	}
	return out, nil
}

// PruneImages removes every unused catalogue image. Docker refuses images that became in
// use in the meantime; such failures are reported, not hidden.
func (m *Manager) PruneImages(ctx context.Context) (PruneResult, error) {
	unused, err := m.UnusedImages(ctx)
	if err != nil {
		return PruneResult{}, err
	}
	res := PruneResult{Removed: []UnusedImage{}, Errors: []string{}}
	for _, img := range unused {
		if err := m.engine.RemoveImage(ctx, img.ID); err != nil {
			if errors.Is(err, docker.ErrNotFound) {
				continue
			}
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", strings.Join(img.Tags, ","), err))
			continue
		}
		res.Removed = append(res.Removed, img)
		res.ReclaimedBytes += img.Size
	}
	m.audit.Log(ctx, audit.ActionImagesPruned, "docker", "", map[string]any{"removed": len(res.Removed), "reclaimedBytes": res.ReclaimedBytes, "errors": len(res.Errors)})
	return res, nil
}

// recordImageChange remembers the image id a container ran before it was recreated
// from a newer local image of the same reference, so the project can be rolled back to
// it. Only the same-reference case counts: a version change is a different image, and a
// container that ran the pinned previous id is just returning to the tag.
func (m *Manager) recordImageChange(ctx context.Context, proj *store.Project, image string, cur docker.Container, newID string) {
	old := proj.ImageRecord(image)
	rec := store.ProjectImage{ProjectID: proj.ID, Image: image, CurrentID: newID}
	switch {
	case cur.ImageID == "" || cur.ImageID == newID:
		return
	case old != nil && cur.ImageID == old.PreviousID:
		// Returning from a rollback: the rollback target stays, the current id moves
		// on if an even newer image was pulled meanwhile.
		if old.CurrentID == newID {
			return
		}
		rec.PreviousID = old.PreviousID
	case cur.Image == image || cur.Image == cur.ImageID:
		// The tag was rebuilt upstream (Docker then lists the container's image as its
		// id, since the tag no longer resolves to it): what ran until now becomes the
		// rollback target.
		rec.PreviousID = cur.ImageID
	default:
		// A different reference: version change, not an update of the same image.
		return
	}
	if err := m.store.Images.Upsert(context.WithoutCancel(ctx), rec); err != nil {
		m.log.Warn("image history not recorded", "project", proj.Slug, "image", image, "err", err)
		return
	}
	// Keep the loaded project in step so a later container in the same plan (workers
	// share the PHP image) sees the record.
	replaced := false
	for i := range proj.Images {
		if proj.Images[i].Image == image {
			proj.Images[i] = rec
			replaced = true
		}
	}
	if !replaced {
		proj.Images = append(proj.Images, rec)
	}
	m.log.Info("image updated", "project", proj.Slug, "image", image, "from", cur.ImageID, "to", newID)
}

// ImageChoice selects which image a project's containers should run for one reference.
type ImageChoice string

const (
	// ImagePrevious rolls back to the image id the containers ran before the last update.
	ImagePrevious ImageChoice = "previous"
	// ImageLatest returns to the current local image of the reference.
	ImageLatest ImageChoice = "latest"
)

// UseImage pins a project's containers to the previous image of ref (rollback) or lifts
// the pin again. Running projects are restarted so the change takes effect; stopped ones
// pick it up at the next start.
func (m *Manager) UseImage(ctx context.Context, id, ref string, choice ImageChoice) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	if choice != ImagePrevious && choice != ImageLatest {
		return View{}, fmt.Errorf("%w: image choice must be %q or %q", validate.ErrInvalid, ImagePrevious, ImageLatest)
	}
	var view View
	err := m.run(ctx, limitProvision, func(ctx context.Context) (err error) {
		view, err = m.useImage(ctx, id, ref, choice)
		return err
	})
	return view, err
}

func (m *Manager) useImage(ctx context.Context, id, ref string, choice ImageChoice) (View, error) {
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	rec := proj.ImageRecord(ref)
	if rec == nil || rec.PreviousID == "" {
		return View{}, fmt.Errorf("%w: no previous image is known for %s", validate.ErrInvalid, ref)
	}
	pin := choice == ImagePrevious
	if pin {
		exists, err := m.engine.ImageExists(ctx, rec.PreviousID)
		if err != nil {
			return View{}, err
		}
		if !exists {
			return View{}, fmt.Errorf("%w: the previous image %s is no longer on this host", validate.ErrInvalid, shortID(rec.PreviousID))
		}
	}
	if err := m.store.Images.SetPinned(ctx, id, ref, pin); err != nil {
		return View{}, err
	}
	rec.Pinned = pin
	action := audit.ActionImageRolledBack
	if !pin {
		action = audit.ActionImageLatest
	}
	m.audit.Log(ctx, action, "project", id, map[string]any{"name": proj.Name, "image": ref, "previous": rec.PreviousID, "current": rec.CurrentID})
	if proj.DesiredState == store.DesiredRunning && proj.Lifecycle == store.LifecycleReady {
		planner, err := m.planner()
		if err != nil {
			return View{}, err
		}
		plan, err := planner.Plan(proj)
		if err != nil {
			return View{}, err
		}
		if err := m.stopPlan(ctx, proj, plan); err != nil {
			return View{}, err
		}
		if err := m.startPlan(ctx, proj, plan); err != nil {
			err = opError(ctx, err)
			_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
			return View{}, err
		}
	}
	return m.Get(context.WithoutCancel(ctx), id)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

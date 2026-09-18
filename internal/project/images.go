package project

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
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
	repos := map[string]bool{}
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

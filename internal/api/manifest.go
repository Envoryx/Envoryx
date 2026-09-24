package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/validate"
)

// The project manifest (envoryx.yml). The CLI sends the file from a local clone as
// text; the web interface works with the copy in the project directory.

type manifestDTO struct {
	FileName string `json:"fileName"`
	// YAML is the project as a manifest.
	YAML string `json:"yaml"`
	// Repository is the envoryx.yml in the project directory compared with the project.
	Repository project.RepositoryManifest `json:"repository"`
}

// getManifest exports a project: GET /projects/{id}/manifest.
func (a *API) getManifest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mf, err := a.d.Projects.ExportManifest(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, err := manifest.Marshal(mf)
	if err != nil {
		writeError(w, r, err)
		return
	}
	repo, err := a.d.Projects.RepositoryManifestStatus(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, manifestDTO{FileName: manifest.FileName, YAML: string(data), Repository: repo})
}

// writeManifest saves the export into the project directory:
// PUT /projects/{id}/manifest/file.
func (a *API) writeManifest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, err := a.d.Projects.WriteRepositoryManifest(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	repo, err := a.d.Projects.RepositoryManifestStatus(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, manifestDTO{FileName: manifest.FileName, YAML: string(data), Repository: repo})
}

type manifestApplyRequest struct {
	// YAML is the manifest; empty uses the envoryx.yml in the project directory.
	YAML    string            `json:"yaml"`
	Prune   bool              `json:"prune"`
	Secrets map[string]string `json:"secrets"`
	Start   bool              `json:"start"`
}

// manifestFrom parses the manifest a request carries, or reads the project's own.
func (a *API) manifestFrom(r *http.Request, id, yaml string) (manifest.Manifest, error) {
	if strings.TrimSpace(yaml) != "" {
		return manifest.Parse([]byte(yaml))
	}
	mf, found, err := a.d.Projects.ReadRepositoryManifest(r.Context(), id)
	if err != nil {
		return manifest.Manifest{}, err
	}
	if !found {
		return manifest.Manifest{}, fmt.Errorf("%w: the project directory has no %s", validate.ErrInvalid, manifest.FileName)
	}
	return mf, nil
}

// planManifest compares a manifest with a project: POST /projects/{id}/manifest/plan.
func (a *API) planManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestApplyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	mf, err := a.manifestFrom(r, id, req.YAML)
	if err != nil {
		writeError(w, r, err)
		return
	}
	plan, err := a.d.Projects.PlanManifest(r.Context(), id, mf, project.ManifestOptions{Prune: req.Prune, Secrets: req.Secrets})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan})
}

// applyManifest brings a project in line with a manifest:
// POST /projects/{id}/manifest/apply.
func (a *API) applyManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestApplyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	mf, err := a.manifestFrom(r, id, req.YAML)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := a.d.Projects.ApplyManifest(r.Context(), id, mf, project.ManifestOptions{Prune: req.Prune, Secrets: req.Secrets, Start: req.Start})
	a.invalidateProxy() // domains may have changed before a later step failed
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": res.Plan, "project": a.project(r, res.View)})
}

type manifestCreateRequest struct {
	YAML    string            `json:"yaml"`
	Name    string            `json:"name"`
	Path    string            `json:"path"`
	Git     *gitRequestDTO    `json:"git"`
	Secrets map[string]string `json:"secrets"`
	Start   bool              `json:"start"`
}

// createFromManifest creates a project from a manifest: POST /projects/from-manifest.
func (a *API) createFromManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	mf, err := manifest.Parse([]byte(req.YAML))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cr := project.ManifestCreateRequest{Name: req.Name, Path: req.Path, Secrets: req.Secrets, Start: req.Start}
	if req.Git != nil && strings.TrimSpace(req.Git.URL) != "" {
		g := req.Git.toDomain()
		g.KeepToken = false
		cr.Git = &g
	}
	res, err := a.d.Projects.CreateFromManifest(r.Context(), mf, cr)
	a.invalidateProxy()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"plan": res.Plan, "project": a.project(r, res.View)})
}

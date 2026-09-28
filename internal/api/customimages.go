package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
)

// setCustomImage gives a runtime service an image of the user's:
// PUT /projects/{id}/services/{kind}/image {"image": "<ref>"} or {"dockerfile": "<path>"}.
func (a *API) setCustomImage(w http.ResponseWriter, r *http.Request) {
	var req project.CustomImageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a.customImage(w, r, req)
}

// removeCustomImage returns a runtime service to the catalogue image.
func (a *API) removeCustomImage(w http.ResponseWriter, r *http.Request) {
	a.customImage(w, r, project.CustomImageRequest{})
}

func (a *API) customImage(w http.ResponseWriter, r *http.Request, req project.CustomImageRequest) {
	view, err := a.d.Projects.SetCustomImage(r.Context(), r.PathValue("id"), store.ServiceKind(r.PathValue("kind")), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

// rebuildCustomImage builds a runtime's Dockerfile again, with fresh base images.
func (a *API) rebuildCustomImage(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.RebuildCustomImage(r.Context(), r.PathValue("id"), store.ServiceKind(r.PathValue("kind")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

// registries lists the private registry logins (without passwords).
func (a *API) registries(w http.ResponseWriter, r *http.Request) {
	regs, err := a.d.Projects.Registries(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registries": regs})
}

// setRegistries replaces the registry logins; an entry without a password keeps the
// stored one of its host.
func (a *API) setRegistries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Registries []project.Registry `json:"registries"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	regs, err := a.d.Projects.SetRegistries(r.Context(), req.Registries)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registries": regs})
}

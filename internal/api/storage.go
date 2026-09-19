package api

import (
	"net/http"
)

// storageInfo describes the project's object storage without credentials (read scope).
func (a *API) storageInfo(w http.ResponseWriter, r *http.Request) {
	info, err := a.d.Projects.StorageInfo(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage": info})
}

// storageCredentials adds the access and secret key (operate scope, like database
// credentials).
func (a *API) storageCredentials(w http.ResponseWriter, r *http.Request) {
	info, err := a.d.Projects.StorageInfo(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage": info})
}

type storagePublicRequest struct {
	PublicRead bool `json:"publicRead"`
}

// storagePublic switches anonymous reads of the bucket on or off.
func (a *API) storagePublic(w http.ResponseWriter, r *http.Request) {
	var req storagePublicRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.SetStoragePublicRead(r.Context(), r.PathValue("id"), req.PublicRead)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage": info})
}

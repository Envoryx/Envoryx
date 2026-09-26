package api

import (
	"net/http"
)

// ollamaModels lists the models in the shared store and the project's downloads.
func (a *API) ollamaModels(w http.ResponseWriter, r *http.Request) {
	models, err := a.d.Projects.OllamaModels(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

// pullOllamaModel starts a model download; its progress shows in ollamaModels.
func (a *API) pullOllamaModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	pull, err := a.d.Projects.PullOllamaModel(r.Context(), r.PathValue("id"), req.Model)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pull": pull})
}

// deleteOllamaModel removes a model from the shared store.
func (a *API) deleteOllamaModel(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.DeleteOllamaModel(r.Context(), r.PathValue("id"), r.PathValue("model")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelOllamaPull stops a running download.
func (a *API) cancelOllamaPull(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.CancelOllamaPull(r.Context(), r.PathValue("id"), r.PathValue("model")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"net/http"
	"time"
)

func (a *API) getShare(w http.ResponseWriter, r *http.Request) {
	s, err := a.d.Projects.ShareStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": s})
}

type shareRequest struct {
	// Minutes the share lasts (0 = an hour).
	Minutes int `json:"minutes"`
}

// startShare puts the project on a public tunnel address. It is admin scope: whoever
// has the address reaches the project from the internet.
func (a *API) startShare(w http.ResponseWriter, r *http.Request) {
	var req shareRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	s, err := a.d.Projects.StartShare(r.Context(), r.PathValue("id"), time.Duration(req.Minutes)*time.Minute)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": s})
}

func (a *API) stopShare(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.StopShare(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

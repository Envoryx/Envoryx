package api

import (
	"net/http"
)

func (a *API) extraServices(w http.ResponseWriter, r *http.Request) {
	extras, err := a.d.Projects.ExtraServices(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": extras})
}

func (a *API) databaseInfo(w http.ResponseWriter, r *http.Request) {
	info, err := a.d.Projects.DatabaseInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"database": info})
}

func (a *API) databaseCredentials(w http.ResponseWriter, r *http.Request) {
	creds, err := a.d.Projects.DatabaseCredentials(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
}

func (a *API) databaseRotate(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.RotateDatabasePassword(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

type databaseExposeRequest struct {
	Exposed bool `json:"exposed"`
}

func (a *API) databaseExpose(w http.ResponseWriter, r *http.Request) {
	var req databaseExposeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.SetDatabaseExposed(r.Context(), r.PathValue("id"), req.Exposed)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

func (a *API) databaseList(w http.ResponseWriter, r *http.Request) {
	names, err := a.d.Projects.ListDatabases(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": names})
}

type databaseNameRequest struct {
	Name    string `json:"name"`
	Confirm string `json:"confirm"`
}

func (a *API) databaseCreate(w http.ResponseWriter, r *http.Request) {
	var req databaseNameRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.CreateDatabase(r.Context(), r.PathValue("id"), req.Name); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (a *API) databaseDrop(w http.ResponseWriter, r *http.Request) {
	var req databaseNameRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.DropDatabase(r.Context(), r.PathValue("id"), r.PathValue("name"), req.Confirm); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

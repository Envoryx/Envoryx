package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/project"
)

func (a *API) extraServices(w http.ResponseWriter, r *http.Request) {
	extras, err := a.d.Projects.ExtraServices(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": extras})
}

// rabbitMQCredentials returns the broker login (operate scope, like database credentials).
func (a *API) rabbitMQCredentials(w http.ResponseWriter, r *http.Request) {
	creds, err := a.d.Projects.RabbitMQCredentials(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
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

func (a *API) databaseSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Projects.ListSnapshots(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

type snapshotRequest struct {
	Note string `json:"note"`
}

func (a *API) databaseSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req snapshotRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.CreateSnapshot(r.Context(), r.PathValue("id"), req.Note)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"snapshot": info})
}

type snapshotRestoreRequest struct {
	Confirm string `json:"confirm"`
}

func (a *API) databaseSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	var req snapshotRestoreRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.RestoreSnapshot(r.Context(), r.PathValue("id"), r.PathValue("snapshot"), req.Confirm)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": info})
}

type cloneDatabaseRequest struct {
	Source string `json:"source"`
	// Snapshot defaults to true: a clone overwrites the target's data, and the request has
	// to say so explicitly to skip the snapshot that makes it undoable.
	Snapshot *bool  `json:"snapshot"`
	Confirm  string `json:"confirm"`
}

func (a *API) databaseClone(w http.ResponseWriter, r *http.Request) {
	var req cloneDatabaseRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	snapshot := req.Snapshot == nil || *req.Snapshot
	res, err := a.d.Projects.CloneDatabase(r.Context(), r.PathValue("id"), project.CloneDatabaseRequest{
		Source: req.Source, Snapshot: snapshot, Confirm: req.Confirm,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clone": res})
}

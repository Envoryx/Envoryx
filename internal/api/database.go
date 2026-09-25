package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
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

// searchCredentials returns the admin key of Meilisearch or Typesense (operate scope,
// like database credentials).
func (a *API) searchCredentials(kind store.ServiceKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		creds, err := a.d.Projects.SearchCredentials(r.Context(), r.PathValue("id"), kind)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
	}
}

// dbParam is the database a request is about: ?db=<name> for an additional one, nothing
// for the primary. A name that cannot exist is "not found", like any unknown database.
func dbParam(r *http.Request) (string, error) {
	db := r.URL.Query().Get("db")
	if db != "" && project.ValidateDatabaseServiceName(db) != nil {
		return "", newError(http.StatusNotFound, "not_found", "unknown database")
	}
	return db, nil
}

// databases lists every database of a project, the primary first.
func (a *API) databases(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Projects.Databases(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": list})
}

func (a *API) databaseInfo(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.DatabaseInfo(r.Context(), r.PathValue("id"), db)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"database": info})
}

func (a *API) databaseCredentials(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	creds, err := a.d.Projects.DatabaseCredentials(r.Context(), r.PathValue("id"), db)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
}

func (a *API) databaseRotate(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.RotateDatabasePassword(r.Context(), r.PathValue("id"), db)
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.SetDatabaseExposed(r.Context(), r.PathValue("id"), db, req.Exposed)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

func (a *API) databaseList(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	names, err := a.d.Projects.ListDatabases(r.Context(), r.PathValue("id"), db)
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.CreateDatabase(r.Context(), r.PathValue("id"), db, req.Name); err != nil {
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.DropDatabase(r.Context(), r.PathValue("id"), db, r.PathValue("name"), req.Confirm); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) databaseSnapshots(w http.ResponseWriter, r *http.Request) {
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list, err := a.d.Projects.ListSnapshots(r.Context(), r.PathValue("id"), db)
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.CreateSnapshot(r.Context(), r.PathValue("id"), db, req.Note)
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.RestoreSnapshot(r.Context(), r.PathValue("id"), db, r.PathValue("snapshot"), req.Confirm)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": info})
}

type cloneDatabaseRequest struct {
	Source string `json:"source"`
	// SourceDB is the source's database to copy; absent = the one named like the target
	// (?db=), "" = its primary.
	SourceDB *string `json:"sourceDb"`
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
	db, err := dbParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.SourceDB != nil && *req.SourceDB != "" && project.ValidateDatabaseServiceName(*req.SourceDB) != nil {
		writeError(w, r, newError(http.StatusNotFound, "not_found", "unknown source database"))
		return
	}
	snapshot := req.Snapshot == nil || *req.Snapshot
	res, err := a.d.Projects.CloneDatabase(r.Context(), r.PathValue("id"), project.CloneDatabaseRequest{
		Source: req.Source, DB: db, SourceDB: req.SourceDB, Snapshot: snapshot, Confirm: req.Confirm,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clone": res})
}

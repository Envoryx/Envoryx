package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Offsite backups: the targets under Settings, the copies next to each backup.

func (a *API) syncer(w http.ResponseWriter, r *http.Request) (*offsite.Syncer, bool) {
	if a.d.Offsite == nil {
		writeError(w, r, newError(http.StatusServiceUnavailable, "not_configured", "offsite backups are not available"))
		return nil, false
	}
	return a.d.Offsite, true
}

// offsiteError maps what talking to a target produced: a refusal of ours stays what it
// is, anything the target or the network said becomes a 502 with the cause, so the UI
// can show "connection refused" instead of an internal error.
func offsiteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, offsite.ErrNotFound), errors.Is(err, offsite.ErrObjectNotFound):
		writeError(w, r, newError(http.StatusNotFound, "not_found", err.Error()))
	case errors.Is(err, offsite.ErrPassphrase):
		writeError(w, r, newError(http.StatusUnprocessableEntity, "wrong_passphrase", err.Error()))
	case errors.Is(err, validate.ErrInvalid), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict),
		errors.Is(err, instance.ErrNotFound), errors.Is(err, project.ErrNotFound), errors.Is(err, project.ErrBusy):
		writeError(w, r, err)
	default:
		writeError(w, r, newError(http.StatusBadGateway, "offsite_failed", err.Error()))
	}
}

type offsiteTargetDTO struct {
	offsite.Target
	Secrets  offsite.Secrets `json:"secrets"`
	Location string          `json:"location"`
	// Last is the newest copy's state on this target.
	Last *offsiteUploadDTO `json:"last,omitempty"`
}

type offsiteUploadDTO struct {
	TargetID      string     `json:"targetId"`
	TargetName    string     `json:"targetName"`
	BackupID      string     `json:"backupId"`
	Scope         string     `json:"scope"`
	Status        string     `json:"status"`
	Error         string     `json:"error,omitempty"`
	RemoteKey     string     `json:"remoteKey,omitempty"`
	SizeBytes     int64      `json:"sizeBytes"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (a *API) uploadDTOs(rows []store.OffsiteUpload) map[string][]offsiteUploadDTO {
	names := map[string]string{}
	if a.d.Offsite != nil {
		for _, t := range a.d.Offsite.Config.Targets() {
			names[t.ID] = t.Name
		}
	}
	out := map[string][]offsiteUploadDTO{}
	for _, u := range rows {
		name, ok := names[u.TargetID]
		if !ok {
			continue // a removed target
		}
		d := offsiteUploadDTO{TargetID: u.TargetID, TargetName: name, BackupID: u.BackupID, Scope: u.Scope, Status: u.Status, Error: u.Error,
			RemoteKey: u.RemoteKey, SizeBytes: u.SizeBytes, Attempts: u.Attempts, UpdatedAt: u.UpdatedAt}
		if !u.NextAttemptAt.IsZero() {
			next := u.NextAttemptAt
			d.NextAttemptAt = &next
		}
		out[u.BackupID] = append(out[u.BackupID], d)
	}
	return out
}

// offsiteTargetNames is what a backup list needs to offer uploads: no settings, no
// secrets, so a read token may see it.
type offsiteTargetName struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Encrypt bool   `json:"encrypt"`
}

func (a *API) offsiteTargetNames() []offsiteTargetName {
	out := []offsiteTargetName{}
	if a.d.Offsite == nil {
		return out
	}
	for _, t := range a.d.Offsite.Config.Targets() {
		out = append(out, offsiteTargetName{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled, Encrypt: t.Encrypt})
	}
	return out
}

// listOffsiteTargets: GET /offsite.
func (a *API) listOffsiteTargets(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	recent, err := a.d.Store.Offsite.Recent(r.Context(), 500)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := []offsiteTargetDTO{}
	for _, t := range s.Config.Targets() {
		d := offsiteTargetDTO{Target: t.Public(), Secrets: t.HasSecrets(), Location: t.Location()}
		for _, u := range recent {
			if u.TargetID == t.ID && (u.Status == store.OffsiteDone || u.Status == store.OffsiteFailed) {
				dto := a.uploadDTOs([]store.OffsiteUpload{u})[u.BackupID][0]
				d.Last = &dto
				break
			}
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// saveOffsiteTarget: POST /offsite/targets and PUT /offsite/targets/{target}.
func (a *API) saveOffsiteTarget(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var t offsite.Target
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, r, err)
		return
	}
	t.ID = r.PathValue("target")
	prepared, err := s.Config.Prepare(t)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	if err := s.Config.Save(prepared); err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if t.ID == "" {
		status = http.StatusCreated
	}
	a.d.Audit.Log(r.Context(), audit.ActionSettingsChanged, "offsite", prepared.ID, map[string]any{"name": prepared.Name, "type": prepared.Type, "location": prepared.Location(), "encrypt": prepared.Encrypt})
	writeJSON(w, status, map[string]any{"target": offsiteTargetDTO{Target: prepared.Public(), Secrets: prepared.HasSecrets(), Location: prepared.Location()}})
}

// deleteOffsiteTarget: DELETE /offsite/targets/{target}. The copies stay on the target.
func (a *API) deleteOffsiteTarget(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	id := r.PathValue("target")
	if err := s.Config.Delete(id); err != nil {
		offsiteError(w, r, err)
		return
	}
	_ = a.d.Store.Offsite.DeleteByTarget(r.Context(), id)
	a.d.Audit.Log(r.Context(), audit.ActionSettingsChanged, "offsite", id, map[string]any{"removed": true})
	w.WriteHeader(http.StatusNoContent)
}

// testOffsiteTarget: POST /offsite/test with a (possibly unsaved) target; the secrets of
// a saved one are filled in when left empty.
func (a *API) testOffsiteTarget(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var t offsite.Target
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, r, err)
		return
	}
	prepared, err := s.Config.Prepare(t)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	res, err := s.Test(r.Context(), prepared)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// listRemoteInstanceBackups: GET /offsite/targets/{target}/instance.
func (a *API) listRemoteInstanceBackups(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	list, err := s.ListInstance(r.Context(), r.PathValue("target"))
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list})
}

type remoteKeyRequest struct {
	Key string `json:"key"`
}

// fetchRemoteInstanceBackup: POST /offsite/targets/{target}/instance/fetch – the first
// step of a disaster recovery; restoring is the usual instance restore afterwards.
func (a *API) fetchRemoteInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var req remoteKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := s.FetchInstance(r.Context(), r.PathValue("target"), req.Key)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceBackupCreated, "instance", info.ID, map[string]any{"bytes": info.SizeBytes, "offsite": req.Key})
	writeJSON(w, http.StatusCreated, map[string]any{"backup": info})
}

// deleteRemoteBackup: POST /offsite/targets/{target}/remove.
func (a *API) deleteRemoteBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var req remoteKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.DeleteRemote(r.Context(), r.PathValue("target"), req.Key); err != nil {
		offsiteError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionBackupDeleted, "offsite", r.PathValue("target"), map[string]any{"key": req.Key})
	w.WriteHeader(http.StatusNoContent)
}

type offsiteUploadRequest struct {
	// Targets are the target ids; empty = every enabled target.
	Targets []string `json:"targets"`
}

// uploadInstanceBackupOffsite: POST /instance/backups/{id}/offsite.
func (a *API) uploadInstanceBackupOffsite(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var req offsiteUploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.UploadInstance(r.Context(), r.PathValue("id"), req.Targets)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"offsite": a.uploadDTOs(rows)[r.PathValue("id")]})
}

// uploadBackupOffsite: POST /projects/{id}/backups/{backup}/offsite.
func (a *API) uploadBackupOffsite(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var req offsiteUploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	backupID := r.PathValue("backup")
	if err := validate.UUID(backupID); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	rows, err := s.UploadProject(r.Context(), r.PathValue("id"), backupID, req.Targets)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"offsite": a.uploadDTOs(rows)[backupID]})
}

// listRemoteBackups: GET /projects/{id}/offsite/{target}.
func (a *API) listRemoteBackups(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	list, err := s.ListProject(r.Context(), r.PathValue("target"), r.PathValue("id"))
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list})
}

// fetchRemoteBackup: POST /projects/{id}/offsite/{target}/fetch – the copy becomes a
// local backup, restored the usual way.
func (a *API) fetchRemoteBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.syncer(w, r)
	if !ok {
		return
	}
	var req remoteKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := s.FetchProject(r.Context(), r.PathValue("target"), r.PathValue("id"), req.Key)
	if err != nil {
		offsiteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"backup": info})
}

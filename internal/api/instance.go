package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/validate"
)

// maxInstanceUpload bounds uploaded instance backups; the config directory is small,
// the limit only stops runaway requests.
const maxInstanceUpload = 2 << 30

func (a *API) instanceStore(w http.ResponseWriter, r *http.Request) (*instance.Store, bool) {
	if a.d.Instance == nil || a.d.DB == nil {
		writeError(w, r, newError(http.StatusServiceUnavailable, "not_configured", "instance backups are not available"))
		return nil, false
	}
	return a.d.Instance, true
}

func (a *API) listInstanceBackups(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	list, err := s.List()
	if err != nil {
		writeError(w, r, err)
		return
	}
	pending, err := s.PendingRestore()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list, "pendingRestore": pending, "dir": s.Dir, "canRestart": a.d.Restart != nil})
}

func (a *API) createInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := s.Create(r.Context(), a.d.DB, instance.KindManual, req.Note)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceBackupCreated, "instance", info.ID, map[string]any{"bytes": info.SizeBytes})
	writeJSON(w, http.StatusCreated, map[string]any{"backup": info})
}

func (a *API) uploadInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInstanceUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, fmt.Errorf("%w: expected a multipart upload", validate.ErrInvalid))
		return
	}
	var src io.Reader
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, r, fmt.Errorf("%w: %v", validate.ErrInvalid, err))
			return
		}
		if part.FormName() == "file" {
			src = part
			break
		}
		_ = part.Close()
	}
	if src == nil {
		writeError(w, r, fmt.Errorf("%w: missing file field", validate.ErrInvalid))
		return
	}
	info, err := s.Import(src)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("%w: upload exceeds %d bytes", validate.ErrInvalid, tooLarge.Limit)
		}
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceBackupUploaded, "instance", info.ID, map[string]any{"bytes": info.SizeBytes, "envoryx": info.Meta.Envoryx})
	writeJSON(w, http.StatusCreated, map[string]any{"backup": info})
}

func (a *API) deleteInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.Delete(id); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceBackupDeleted, "instance", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) downloadInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	rc, name, size, err := s.Open(r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// restoreInstanceBackup schedules the restore and restarts Envoryx to apply it. The
// response goes out before the restart; the UI waits for the server to come back.
func (a *API) restoreInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Confirm != "restore" {
		writeError(w, r, fmt.Errorf("%w: type restore to confirm", validate.ErrInvalid))
		return
	}
	id := r.PathValue("id")
	if err := s.ScheduleRestore(id); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceRestore, "instance", id, map[string]any{"restart": a.d.Restart != nil})
	writeJSON(w, http.StatusAccepted, map[string]any{"scheduled": id, "restarting": a.d.Restart != nil})
	if a.d.Restart != nil {
		a.d.Restart()
	}
}

func (a *API) cancelInstanceRestore(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	if err := s.CancelRestore(); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

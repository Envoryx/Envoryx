package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
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
	ids := make([]string, len(list))
	for i, b := range list {
		ids[i] = b.ID
	}
	rows, err := a.d.Store.Offsite.ByBackups(r.Context(), store.OffsiteInstance, ids)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list, "pendingRestore": pending, "dir": s.Dir, "canRestart": a.d.Restart != nil,
		"offsite": a.uploadDTOs(rows), "offsiteTargets": a.offsiteTargetNames()})
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
		// Like a failed project backup, and like the daily one for the offsite targets:
		// whoever watches the notifications learns of it, not only the one who asked.
		if a.d.Notify != nil && !errors.Is(err, validate.ErrInvalid) && !errors.Is(err, instance.ErrPending) {
			a.d.Notify.Notify(r.Context(), notify.Event{Kind: "backup.failed", Level: notify.Error, Title: "Instance backup failed", Message: err.Error()})
		}
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
// errRestoreNeedsSession keeps an API token from restoring an instance backup the
// instance didn't make itself: an uploaded or fetched archive can carry accounts and
// tokens of anyone's choosing, a way in that would outlast the token.
var errRestoreNeedsSession = newError(http.StatusForbidden, "session_required", "API tokens can only restore instance backups this instance made; restore an uploaded or fetched one from a browser session")

func (a *API) restoreInstanceBackup(w http.ResponseWriter, r *http.Request) {
	s, ok := a.instanceStore(w, r)
	if !ok {
		return
	}
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" {
		info, err := s.Get(r.PathValue("id"))
		if err != nil {
			writeError(w, r, err)
			return
		}
		if !info.Own() {
			writeError(w, r, errRestoreNeedsSession)
			return
		}
	}
	var req struct {
		Confirm string `json:"confirm"`
		// Key is the backup's secret key when it differs from the running instance's.
		Key string `json:"key"`
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
	if err := a.checkInstanceRestorePlan(s, id); err != nil {
		writeError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	if err := s.ScheduleRestore(id, audit.Actor(p), req.Key); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionInstanceRestore, "instance", id, map[string]any{"restart": a.d.Restart != nil})
	writeJSON(w, http.StatusAccepted, map[string]any{"scheduled": id, "restarting": a.d.Restart != nil})
	if a.d.Restart != nil {
		a.d.Restart()
	}
}

// errForeignInstanceBackup refuses another instance's backup on a hosted instance.
const errForeignInstanceBackup = "on a hosted instance only its own instance backups can be restored; bring projects from elsewhere over as project backups (New project → Existing website), where the plan is checked"

// checkInstanceRestorePlan keeps a hosted instance within its plan: an instance backup of
// another Envoryx brings that instance's projects with whatever services they run, past
// the checks a project, a service or a project backup goes through. Its own backups
// bring back what it had, which the plan already allowed or left in place.
func (a *API) checkInstanceRestorePlan(s *instance.Store, id string) error {
	if a.d.Projects == nil || a.d.Projects.Plan() == nil {
		return nil
	}
	info, err := s.Get(id)
	if err != nil {
		return err
	}
	if info.Own() {
		return nil
	}
	if a.d.Engine != nil {
		if own := a.d.Engine.InstanceID(); own != "" && info.Meta.InstanceID == own {
			return nil // made here, then downloaded or fetched from an offsite target
		}
	}
	return plan.Quota("%s", errForeignInstanceBackup)
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

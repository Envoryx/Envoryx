package api

import (
	"io"
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
)

func (a *API) listBackups(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Projects.ListBackups(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	ids := make([]string, len(list))
	for i, b := range list {
		ids[i] = b.ID
	}
	rows, err := a.d.Store.Offsite.ByBackups(r.Context(), store.OffsiteProject, ids)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list, "offsite": a.uploadDTOs(rows), "offsiteTargets": a.offsiteTargetNames()})
}

type createBackupRequest struct {
	Database            bool   `json:"database"`
	Files               bool   `json:"files"`
	Storage             bool   `json:"storage"`
	IncludeDependencies bool   `json:"includeDependencies"`
	Note                string `json:"note"`
	// Offsite copies the new backup to every enabled offsite target.
	Offsite bool `json:"offsite"`
}

func (a *API) createBackup(w http.ResponseWriter, r *http.Request) {
	var req createBackupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.CreateBackup(r.Context(), r.PathValue("id"), project.BackupOptions{
		Database: req.Database, Files: req.Files, Storage: req.Storage, IncludeDependencies: req.IncludeDependencies, Note: req.Note,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := map[string]any{"backup": info}
	if req.Offsite && a.d.Offsite != nil {
		// The backup exists either way; a refused upload (no target enabled) is reported
		// next to it rather than as the request's failure.
		if rows, err := a.d.Offsite.UploadProject(r.Context(), r.PathValue("id"), info.ID, nil); err != nil {
			body["offsiteError"] = err.Error()
		} else {
			body["offsite"] = a.uploadDTOs(rows)[info.ID]
		}
	}
	writeJSON(w, http.StatusCreated, body)
}

func (a *API) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.DeleteBackup(r.Context(), r.PathValue("id"), r.PathValue("backup")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type restoreBackupRequest struct {
	Database    bool   `json:"database"`
	Files       bool   `json:"files"`
	Storage     bool   `json:"storage"`
	WipeFiles   bool   `json:"wipeFiles"`
	WipeStorage bool   `json:"wipeStorage"`
	Confirm     string `json:"confirm"`
}

func (a *API) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var req restoreBackupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := a.d.Projects.RestoreBackup(r.Context(), r.PathValue("id"), r.PathValue("backup"), project.RestoreOptions{Database: req.Database, Files: req.Files, Storage: req.Storage, WipeFiles: req.WipeFiles, WipeStorage: req.WipeStorage, Confirm: req.Confirm})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup": info})
}

func (a *API) downloadBackup(w http.ResponseWriter, r *http.Request) {
	rc, name, err := a.d.Projects.OpenBackupArchive(r.Context(), r.PathValue("id"), r.PathValue("backup"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

type backupScheduleDTO struct {
	Schedule            string     `json:"schedule"`
	Hour                int        `json:"hour"`
	Weekday             int        `json:"weekday"`
	Keep                int        `json:"keep"`
	IncludeDependencies bool       `json:"includeDependencies"`
	LastRun             *time.Time `json:"lastRun,omitempty"`
}

func toSchedule(b store.BackupSchedule) backupScheduleDTO {
	dto := backupScheduleDTO{Schedule: b.Schedule, Hour: b.Hour, Weekday: b.Weekday, Keep: b.Keep, IncludeDependencies: b.IncludeDependencies}
	if !b.LastRun.IsZero() {
		lr := b.LastRun
		dto.LastRun = &lr
	}
	return dto
}

func (a *API) setBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var req backupScheduleDTO
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	b, err := a.d.Projects.SetBackupSchedule(r.Context(), r.PathValue("id"), store.BackupSchedule{Schedule: req.Schedule, Hour: req.Hour, Weekday: req.Weekday, Keep: req.Keep, IncludeDependencies: req.IncludeDependencies})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": toSchedule(b)})
}

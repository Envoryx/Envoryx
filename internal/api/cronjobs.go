package api

import (
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
)

type cronRunDTO struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	ExitCode   int        `json:"exitCode"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Output and Truncated are only part of the runs endpoint (operate scope): output can
	// carry whatever the command prints.
	Output    *string `json:"output,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
}

func toCronRun(r store.CronRun, withOutput bool) cronRunDTO {
	dto := cronRunDTO{ID: r.ID, Source: r.Source, Status: r.Status, ExitCode: r.ExitCode, StartedAt: r.StartedAt}
	if !r.FinishedAt.IsZero() {
		t := r.FinishedAt
		dto.FinishedAt = &t
	}
	if withOutput {
		out := r.Output
		dto.Output, dto.Truncated = &out, r.Truncated
	}
	return dto
}

type cronJobDTO struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Runtime        string      `json:"runtime"`
	Schedule       string      `json:"schedule"`
	Command        string      `json:"command"`
	TimeoutSeconds int         `json:"timeoutSeconds"`
	Enabled        bool        `json:"enabled"`
	NextRun        *time.Time  `json:"nextRun,omitempty"`
	Running        bool        `json:"running"`
	RuntimeMissing bool        `json:"runtimeMissing"`
	LastRun        *cronRunDTO `json:"lastRun,omitempty"`
	CreatedAt      time.Time   `json:"createdAt"`
}

func toCronJob(j project.CronJobInfo) cronJobDTO {
	dto := cronJobDTO{ID: j.ID, Name: j.Name, Runtime: j.Runtime, Schedule: j.Schedule, Command: j.Command, TimeoutSeconds: int(j.Timeout / time.Second),
		Enabled: j.Enabled, NextRun: j.NextRun, Running: j.Running, RuntimeMissing: j.RuntimeMissing, CreatedAt: j.CreatedAt}
	if j.LastRun != nil {
		run := toCronRun(*j.LastRun, false)
		dto.LastRun = &run
	}
	return dto
}

// cronTimezone names the zone schedules are read in: Envoryx's own (TZ).
func cronTimezone() string {
	if name := time.Local.String(); name != "Local" {
		return name
	}
	name, _ := time.Now().Zone()
	return name
}

func (a *API) listCronJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.d.Projects.CronJobs(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]cronJobDTO, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toCronJob(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out, "timezone": cronTimezone()})
}

type cronJobRequest struct {
	Name           string `json:"name"`
	Runtime        string `json:"runtime"`
	Schedule       string `json:"schedule"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	Enabled        bool   `json:"enabled"`
}

func (c cronJobRequest) toDomain() project.CronJobRequest {
	return project.CronJobRequest{Name: c.Name, Runtime: c.Runtime, Schedule: c.Schedule, Command: c.Command, Timeout: time.Duration(c.TimeoutSeconds) * time.Second, Enabled: c.Enabled}
}

func (a *API) addCronJob(w http.ResponseWriter, r *http.Request) {
	var req cronJobRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	j, err := a.d.Projects.AddCronJob(r.Context(), r.PathValue("id"), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"job": toCronJob(j)})
}

func (a *API) updateCronJob(w http.ResponseWriter, r *http.Request) {
	var req cronJobRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	j, err := a.d.Projects.UpdateCronJob(r.Context(), r.PathValue("id"), r.PathValue("job"), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toCronJob(j)})
}

func (a *API) removeCronJob(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.RemoveCronJob(r.Context(), r.PathValue("id"), r.PathValue("job")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runCronJob starts a run and answers right away; the run continues in the background.
func (a *API) runCronJob(w http.ResponseWriter, r *http.Request) {
	run, err := a.d.Projects.RunCronJobNow(r.Context(), r.PathValue("id"), r.PathValue("job"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": toCronRun(run, false)})
}

func (a *API) cronRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := a.d.Projects.CronRuns(r.Context(), r.PathValue("id"), r.PathValue("job"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]cronRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, toCronRun(run, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// cronPreview validates a schedule and lists its next fire times, for the form.
func (a *API) cronPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Schedule string `json:"schedule"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	next, err := project.CronPreview(req.Schedule, 5, time.Now())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next, "timezone": cronTimezone()})
}

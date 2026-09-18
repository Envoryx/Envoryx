package api

import (
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
)

type workerDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Preset    string    `json:"preset"`
	Arg       string    `json:"arg"`
	Enabled   bool      `json:"enabled"`
	Command   []string  `json:"command"`
	CreatedAt time.Time `json:"createdAt"`
}

func toWorker(w store.Worker) workerDTO {
	dto := workerDTO{ID: w.ID, Name: w.Name, Preset: w.Preset, Enabled: w.Enabled, CreatedAt: w.CreatedAt, Command: []string{}}
	if len(w.Args) > 0 {
		dto.Arg = w.Args[0]
	}
	if cmd, err := project.WorkerCommand(w); err == nil {
		dto.Command = cmd
	}
	return dto
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]workerDTO, 0, len(view.Project.Workers))
	for _, wk := range view.Project.Workers {
		out = append(out, toWorker(wk))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": out, "presets": project.WorkerPresets()})
}

type workerRequest struct {
	Name    string `json:"name"`
	Preset  string `json:"preset"`
	Arg     string `json:"arg"`
	Enabled bool   `json:"enabled"`
}

func (a *API) addWorker(w http.ResponseWriter, r *http.Request) {
	var req workerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	wk, err := a.d.Projects.AddWorker(r.Context(), r.PathValue("id"), project.WorkerRequest{Name: req.Name, Preset: req.Preset, Arg: req.Arg, Enabled: req.Enabled})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"worker": toWorker(wk)})
}

func (a *API) updateWorker(w http.ResponseWriter, r *http.Request) {
	var req workerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	wk, err := a.d.Projects.UpdateWorker(r.Context(), r.PathValue("id"), r.PathValue("worker"), project.WorkerRequest{Name: req.Name, Preset: req.Preset, Arg: req.Arg, Enabled: req.Enabled})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker": toWorker(wk)})
}

func (a *API) removeWorker(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.RemoveWorker(r.Context(), r.PathValue("id"), r.PathValue("worker")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) stopIDEBackend(w http.ResponseWriter, r *http.Request) {
	n, err := a.d.Projects.StopIDEBackend(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": n})
}

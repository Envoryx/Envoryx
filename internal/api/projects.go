package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
)

// ---- DTOs -----------------------------------------------------------------

type serviceDTO struct {
	Kind    string          `json:"kind"`
	Variant string          `json:"variant"`
	Version string          `json:"version"`
	Image   string          `json:"image"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
}

type envDTO struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

type serviceStatusDTO struct {
	Kind          string    `json:"kind"`
	Variant       string    `json:"variant"`
	Version       string    `json:"version"`
	Image         string    `json:"image"`
	ContainerName string    `json:"containerName"`
	ContainerID   string    `json:"containerId,omitempty"`
	Exists        bool      `json:"exists"`
	Running       bool      `json:"running"`
	State         string    `json:"state"`
	Status        string    `json:"status,omitempty"`
	Health        string    `json:"health,omitempty"`
	Ports         []portDTO `json:"ports"`
}

type portDTO struct {
	HostIP        string `json:"hostIp"`
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type statusDTO struct {
	State    string             `json:"state"`
	Services []serviceStatusDTO `json:"services"`
	Warnings []string           `json:"warnings"`
}

type projectDTO struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Slug         string       `json:"slug"`
	Path         string       `json:"path"`
	Docroot      string       `json:"docroot"`
	DesiredState string       `json:"desiredState"`
	Lifecycle    string       `json:"lifecycle"`
	LastError    string       `json:"lastError,omitempty"`
	HTTPPort     int          `json:"httpPort"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	Services     []serviceDTO `json:"services"`
	Env          []envDTO     `json:"env"`
	Status       statusDTO    `json:"status"`
}

func toPorts(in []docker.PortMapping) []portDTO {
	out := make([]portDTO, 0, len(in))
	for _, p := range in {
		out = append(out, portDTO{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
	}
	return out
}

func toStatus(st project.Status) statusDTO {
	out := statusDTO{State: string(st.State), Services: []serviceStatusDTO{}, Warnings: st.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, s := range st.Services {
		out.Services = append(out.Services, serviceStatusDTO{
			Kind: string(s.Kind), Variant: s.Variant, Version: s.Version, Image: s.Image, ContainerName: s.ContainerName,
			ContainerID: s.ContainerID, Exists: s.Exists, Running: s.Running, State: s.State, Status: s.Status, Health: s.Health, Ports: toPorts(s.Ports),
		})
	}
	return out
}

// redactedConfig returns a service configuration safe for API responses: database
// credentials are stripped, everything else is passed through.
func redactedConfig(s store.ProjectService) json.RawMessage {
	if s.Kind == store.ServiceDatabase {
		var cfg runtime.DatabaseConfig
		if err := json.Unmarshal(s.Config, &cfg); err == nil {
			if b, err := json.Marshal(cfg.Redacted()); err == nil {
				return b
			}
		}
		return json.RawMessage("{}")
	}
	if len(s.Config) == 0 {
		return json.RawMessage("{}")
	}
	return s.Config
}

func toProject(v project.View) projectDTO {
	p := v.Project
	dto := projectDTO{
		ID: p.ID, Name: p.Name, Slug: p.Slug, Path: p.Path, Docroot: p.Docroot,
		DesiredState: string(p.DesiredState), Lifecycle: string(p.Lifecycle), LastError: p.LastError,
		HTTPPort: p.HTTPPort, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Services: []serviceDTO{}, Env: []envDTO{}, Status: toStatus(v.Status),
	}
	for _, s := range p.Services {
		dto.Services = append(dto.Services, serviceDTO{Kind: string(s.Kind), Variant: s.Variant, Version: s.Version, Image: s.Image, Enabled: s.Enabled, Config: redactedConfig(s)})
	}
	for _, e := range p.Env {
		dto.Env = append(dto.Env, envDTO{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	return dto
}

// ---- Requests ---------------------------------------------------------------

type phpRequestDTO struct {
	Version string            `json:"version"`
	Config  runtime.PHPConfig `json:"config"`
}

type databaseRequestDTO struct {
	Type       string `json:"type"`
	Version    string `json:"version"`
	ExposePort bool   `json:"exposePort"`
}

type databaseUpdateDTO struct {
	Enabled    bool   `json:"enabled"`
	Type       string `json:"type"`
	Version    string `json:"version"`
	ExposePort bool   `json:"exposePort"`
	RemoveData bool   `json:"removeData"`
}

type createProjectRequest struct {
	Name     string              `json:"name"`
	Path     string              `json:"path"`
	Docroot  string              `json:"docroot"`
	PHP      *phpRequestDTO      `json:"php"`
	Database *databaseRequestDTO `json:"database"`
	Web      *struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	} `json:"web"`
	Env           []envDTO `json:"env"`
	CreateStarter bool     `json:"createStarter"`
	Start         bool     `json:"start"`
}

func (r createProjectRequest) toDomain() project.CreateRequest {
	req := project.CreateRequest{Name: r.Name, Path: r.Path, Docroot: r.Docroot, CreateStarter: r.CreateStarter, Start: r.Start}
	if r.PHP != nil {
		req.PHP = &project.PHPRequest{Version: r.PHP.Version, Config: r.PHP.Config}
	}
	if r.Database != nil {
		req.Database = &project.DatabaseRequest{Type: r.Database.Type, Version: r.Database.Version, ExposePort: r.Database.ExposePort}
	}
	if r.Web != nil {
		req.Web = project.WebRequest{Type: r.Web.Type, Version: r.Web.Version}
	}
	for _, e := range r.Env {
		req.Env = append(req.Env, project.EnvVarRequest{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	return req
}

type updateProjectRequest struct {
	Name     *string            `json:"name"`
	Docroot  *string            `json:"docroot"`
	PHP      *phpRequestDTO     `json:"php"`
	Database *databaseUpdateDTO `json:"database"`
	Env      *[]envDTO          `json:"env"`
}

type deleteProjectRequest struct {
	Confirm     string `json:"confirm"`
	DeleteFiles bool   `json:"deleteFiles"`
}

// ---- Handlers ---------------------------------------------------------------

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	views, err := a.d.Projects.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]projectDTO, 0, len(views))
	for _, v := range views {
		out = append(out, toProject(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.Create(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": toProject(view)})
}

func (a *API) previewProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	pv, err := a.d.Projects.Preview(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": pv})
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProject(view)})
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var req updateProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	upd := project.UpdateRequest{Name: req.Name, Docroot: req.Docroot}
	if req.PHP != nil {
		upd.PHP = &project.PHPRequest{Version: req.PHP.Version, Config: req.PHP.Config}
	}
	if req.Database != nil {
		upd.Database = &project.DatabaseUpdate{Enabled: req.Database.Enabled, Type: req.Database.Type, Version: req.Database.Version, ExposePort: req.Database.ExposePort, RemoveData: req.Database.RemoveData}
	}
	if req.Env != nil {
		env := make([]project.EnvVarRequest, 0, len(*req.Env))
		for _, e := range *req.Env {
			env = append(env, project.EnvVarRequest{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
		}
		upd.Env = &env
	}
	view, err := a.d.Projects.Update(r.Context(), r.PathValue("id"), upd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProject(view)})
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	var req deleteProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Projects.Delete(r.Context(), r.PathValue("id"), project.DeleteOptions{Confirm: req.Confirm, DeleteFiles: req.DeleteFiles}); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) startProject(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.d.Projects.Start)
}

func (a *API) stopProject(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.d.Projects.Stop)
}

func (a *API) restartProject(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.d.Projects.Restart)
}

func (a *API) transition(w http.ResponseWriter, r *http.Request, op func(ctx context.Context, id string) (project.View, error)) {
	view, err := op(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProject(view)})
}

func (a *API) projectServices(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": toStatus(view.Status).Services})
}

func (a *API) projectPlan(w http.ResponseWriter, r *http.Request) {
	pv, err := a.d.Projects.PlanFor(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": pv})
}

func (a *API) projectStats(w http.ResponseWriter, r *http.Request) {
	view, err := a.d.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	summary, err := a.d.Stats.Summary(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	usage := summary.PerProject[view.Project.ID]
	writeJSON(w, http.StatusOK, map[string]any{"stats": usage, "sampledAt": summary.SampledAt})
}

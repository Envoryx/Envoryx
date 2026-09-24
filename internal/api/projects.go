package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/envoryx/envoryx/internal/auth"
	"net/http"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/stats"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
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
	// WorkerID is set for worker containers.
	WorkerID string `json:"workerId,omitempty"`
	// Image history: when the containers were last recreated from a rebuilt image, whether
	// the previous one is still available (rollback), and whether it is pinned right now.
	ImageChangedAt *time.Time `json:"imageChangedAt,omitempty"`
	ImagePrevious  bool       `json:"imagePrevious"`
	ImagePinned    bool       `json:"imagePinned"`
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
	// Operation is the lifecycle action running on the project right now, if any.
	Operation *project.Operation `json:"operation,omitempty"`
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
	Git          gitDTO       `json:"git"`
	Hostnames    []string     `json:"hostnames"`
	// DevHostname is set when the Node dev server is enabled (routed by the proxy); it is
	// also the primary route when the project has no PHP.
	DevHostname    string            `json:"devHostname,omitempty"`
	BackupSchedule backupScheduleDTO `json:"backupSchedule"`
	IDEGateway     bool              `json:"ideGateway"`
	// Limits cap CPU, memory and processes of the containers (zero values: none).
	Limits store.ResourceLimits `json:"limits"`
	// Serves says what the primary host name reaches: "php", "python" (application
	// server), "node" (dev server) or "static"; AppService is the application container's
	// kind (php, python, node), absent for static sites.
	Serves     string `json:"serves"`
	AppService string `json:"appService,omitempty"`
}

type gitDTO struct {
	URL      string `json:"url"`
	Branch   string `json:"branch"`
	Username string `json:"username"`
	HasToken bool   `json:"hasToken"`
}

func toPorts(in []docker.PortMapping) []portDTO {
	out := make([]portDTO, 0, len(in))
	for _, p := range in {
		out = append(out, portDTO{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
	}
	return out
}

func toStatus(st project.Status) statusDTO {
	out := statusDTO{State: string(st.State), Services: []serviceStatusDTO{}, Warnings: st.Warnings, Operation: st.Operation}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, s := range st.Services {
		out.Services = append(out.Services, serviceStatusDTO{
			Kind: string(s.Kind), Variant: s.Variant, Version: s.Version, Image: s.Image, ContainerName: s.ContainerName,
			ContainerID: s.ContainerID, Exists: s.Exists, Running: s.Running, State: s.State, Status: s.Status, Health: s.Health, Ports: toPorts(s.Ports),
			WorkerID: s.WorkerID, ImageChangedAt: s.ImageChangedAt, ImagePrevious: s.ImagePrevious, ImagePinned: s.ImagePinned,
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
		Git:    gitDTO{URL: p.Git.URL, Branch: p.Git.Branch, Username: p.Git.Username, HasToken: p.Git.Token != ""},
		Serves: project.Serves(p),
	}
	if kind, ok := project.AppKind(p); ok {
		dto.AppService = string(kind)
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

// phpRequestDTO selects the PHP runtime. Omitting "php" from a create request means no
// PHP.
type phpRequestDTO struct {
	Version string            `json:"version"`
	Config  runtime.PHPConfig `json:"config"`
}

// phpUpdateDTO changes the PHP service; "enabled" (default true) adds PHP to a project
// without it or, when false, removes the PHP container (workers' containers go with it).
type phpUpdateDTO struct {
	Enabled *bool `json:"enabled"`
	phpRequestDTO
}

type nodeRequestDTO struct {
	Version string `json:"version"`
	// Dev-server options (see runtime.NodeConfig).
	DevServer      bool   `json:"devServer"`
	Mode           string `json:"mode"`
	PackageManager string `json:"packageManager"`
	Script         string `json:"script"`
	BuildScript    string `json:"buildScript"`
	Port           int    `json:"port"`
	Preset         string `json:"preset"`
	Inspect        bool   `json:"inspect"`
	InspectPort    int    `json:"inspectPort"`
}

func (n nodeRequestDTO) config() runtime.NodeConfig {
	return runtime.NodeConfig{DevServer: n.DevServer, Mode: n.Mode, PackageManager: n.PackageManager, Script: n.Script, BuildScript: n.BuildScript, Port: n.Port, Preset: n.Preset, Inspect: n.Inspect, InspectPort: n.InspectPort}
}

type nodeUpdateDTO struct {
	Enabled bool `json:"enabled"`
	nodeRequestDTO
}

type pythonRequestDTO struct {
	Version string `json:"version"`
	// Application-server options (see runtime.PythonConfig).
	Server    bool   `json:"server"`
	Mode      string `json:"mode"`
	Preset    string `json:"preset"`
	App       string `json:"app"`
	Port      int    `json:"port"`
	Debug     bool   `json:"debug"`
	DebugPort int    `json:"debugPort"`
}

func (n pythonRequestDTO) config() runtime.PythonConfig {
	return runtime.PythonConfig{Server: n.Server, Mode: n.Mode, Preset: n.Preset, App: n.App, Port: n.Port, Debug: n.Debug, DebugPort: n.DebugPort}
}

type pythonUpdateDTO struct {
	Enabled bool `json:"enabled"`
	pythonRequestDTO
}

type extraRequestDTO struct {
	Version    string `json:"version"`
	ExposePort bool   `json:"exposePort"`
	Dashboards bool   `json:"dashboards"` // OpenSearch only
}

type extraUpdateDTO struct {
	Enabled    bool   `json:"enabled"`
	Version    string `json:"version"`
	ExposePort bool   `json:"exposePort"`
	RemoveData bool   `json:"removeData"`
	Dashboards *bool  `json:"dashboards"` // OpenSearch only; null leaves it
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
	Name          string              `json:"name"`
	Path          string              `json:"path"`
	Docroot       string              `json:"docroot"`
	PHP           *phpRequestDTO      `json:"php"`
	Node          *nodeRequestDTO     `json:"node"`
	Python        *pythonRequestDTO   `json:"python"`
	Database      *databaseRequestDTO `json:"database"`
	Redis         *extraRequestDTO    `json:"redis"`
	Mailpit       *extraRequestDTO    `json:"mailpit"`
	RabbitMQ      *extraRequestDTO    `json:"rabbitmq"`
	Memcached     *extraRequestDTO    `json:"memcached"`
	Meilisearch   *extraRequestDTO    `json:"meilisearch"`
	Typesense     *extraRequestDTO    `json:"typesense"`
	OpenSearch    *extraRequestDTO    `json:"opensearch"`
	Storage       *storageRequestDTO  `json:"storage"`
	Git           *gitRequestDTO      `json:"git"`
	Web           *webRequestDTO      `json:"web"`
	Env           []envDTO            `json:"env"`
	Template      string              `json:"template"`
	CreateStarter bool                `json:"createStarter"`
	Start         bool                `json:"start"`
	// UseManifest applies the envoryx.yml the cloned repository brings (it wins over the
	// services chosen here). Only with git.
	UseManifest bool `json:"useManifest"`
}

type webRequestDTO struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	// SPAFallback serves /index.html for unknown paths (projects without PHP only); nil
	// leaves it unchanged.
	SPAFallback *bool `json:"spaFallback"`
}

type storageRequestDTO struct {
	Version    string `json:"version"`
	PublicRead *bool  `json:"publicRead"`
}

type storageUpdateDTO struct {
	Enabled    bool   `json:"enabled"`
	Version    string `json:"version"`
	PublicRead *bool  `json:"publicRead"`
	RemoveData bool   `json:"removeData"`
}

func (r createProjectRequest) toDomain() project.CreateRequest {
	req := project.CreateRequest{Name: r.Name, Path: r.Path, Docroot: r.Docroot, Template: strings.TrimSpace(r.Template), CreateStarter: r.CreateStarter, Start: r.Start}
	if r.PHP != nil {
		req.PHP = &project.PHPRequest{Version: r.PHP.Version, Config: r.PHP.Config}
	}
	if r.Node != nil {
		req.Node = &project.NodeRequest{Version: r.Node.Version, Config: r.Node.config()}
	}
	if r.Python != nil {
		req.Python = &project.PythonRequest{Version: r.Python.Version, Config: r.Python.config()}
	}
	if r.Database != nil {
		req.Database = &project.DatabaseRequest{Type: r.Database.Type, Version: r.Database.Version, ExposePort: r.Database.ExposePort}
	}
	if r.Redis != nil {
		req.Redis = &project.ExtraRequest{Version: r.Redis.Version, ExposePort: r.Redis.ExposePort}
	}
	if r.Mailpit != nil {
		req.Mailpit = &project.ExtraRequest{Version: r.Mailpit.Version}
	}
	if r.Memcached != nil {
		req.Memcached = &project.ExtraRequest{Version: r.Memcached.Version, ExposePort: r.Memcached.ExposePort}
	}
	if r.RabbitMQ != nil {
		req.RabbitMQ = &project.ExtraRequest{Version: r.RabbitMQ.Version, ExposePort: r.RabbitMQ.ExposePort}
	}
	if r.Meilisearch != nil {
		req.Meilisearch = &project.ExtraRequest{Version: r.Meilisearch.Version}
	}
	if r.Typesense != nil {
		req.Typesense = &project.ExtraRequest{Version: r.Typesense.Version, ExposePort: r.Typesense.ExposePort}
	}
	if r.OpenSearch != nil {
		req.OpenSearch = &project.ExtraRequest{Version: r.OpenSearch.Version, ExposePort: r.OpenSearch.ExposePort, Dashboards: r.OpenSearch.Dashboards}
	}
	if r.Storage != nil {
		req.Storage = &project.StorageRequest{Version: r.Storage.Version, PublicRead: r.Storage.PublicRead}
	}
	if r.Git != nil && strings.TrimSpace(r.Git.URL) != "" {
		g := r.Git.toDomain()
		g.KeepToken = false
		req.Git = &g
	}
	if r.Web != nil {
		req.Web = project.WebRequest{Type: r.Web.Type, Version: r.Web.Version, SPAFallback: r.Web.SPAFallback}
	}
	for _, e := range r.Env {
		req.Env = append(req.Env, project.EnvVarRequest{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	return req
}

type updateProjectRequest struct {
	Name        *string            `json:"name"`
	Docroot     *string            `json:"docroot"`
	Web         *webRequestDTO     `json:"web"`
	PHP         *phpUpdateDTO      `json:"php"`
	Node        *nodeUpdateDTO     `json:"node"`
	Python      *pythonUpdateDTO   `json:"python"`
	Database    *databaseUpdateDTO `json:"database"`
	Redis       *extraUpdateDTO    `json:"redis"`
	Mailpit     *extraUpdateDTO    `json:"mailpit"`
	RabbitMQ    *extraUpdateDTO    `json:"rabbitmq"`
	Memcached   *extraUpdateDTO    `json:"memcached"`
	Meilisearch *extraUpdateDTO    `json:"meilisearch"`
	Typesense   *extraUpdateDTO    `json:"typesense"`
	OpenSearch  *extraUpdateDTO    `json:"opensearch"`
	Storage     *storageUpdateDTO  `json:"storage"`
	Env         *[]envDTO          `json:"env"`
	IDEGateway  *bool              `json:"ideGateway"`
}

// duplicateProjectRequest copies an existing project. The parts default to "everything
// the original has": a bare {"name": "shop-test"} is the whole project, files, database
// and bucket included.
type duplicateProjectRequest struct {
	Name                string `json:"name"`
	Path                string `json:"path"`
	Files               *bool  `json:"files"`
	IncludeDependencies bool   `json:"includeDependencies"`
	Database            *bool  `json:"database"`
	Storage             *bool  `json:"storage"`
	Workers             *bool  `json:"workers"`
	Git                 *bool  `json:"git"`
	Start               bool   `json:"start"`
}

func (r duplicateProjectRequest) toDomain() project.DuplicateRequest {
	on := func(b *bool) bool { return b == nil || *b }
	return project.DuplicateRequest{
		Name: r.Name, Path: r.Path,
		Files: on(r.Files), IncludeDependencies: r.IncludeDependencies,
		Database: on(r.Database), Storage: on(r.Storage), Workers: on(r.Workers), Git: on(r.Git),
		Start: r.Start,
	}
}

// renameProjectRequest renames a project and everything derived from its identifier.
type renameProjectRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Confirm must equal the current identifier.
	Confirm string `json:"confirm"`
	// KeepDataNames leaves the database, its login and the bucket as they are.
	KeepDataNames bool `json:"keepDataNames"`
}

type deleteProjectRequest struct {
	Confirm     string `json:"confirm"`
	DeleteFiles bool   `json:"deleteFiles"`
}

// ---- Handlers ---------------------------------------------------------------

// withHostnames fills the derived + extra host names of a project DTO.
func (a *API) withHostnames(r *http.Request, dto projectDTO, p store.Project) projectDTO {
	hosts, err := a.projectHosts(r, p)
	dto.Hostnames = []string{}
	if err == nil {
		for _, h := range hosts {
			dto.Hostnames = append(dto.Hostnames, h.Hostname)
		}
	}
	dto.BackupSchedule = toSchedule(p.Backup)
	dto.IDEGateway = p.IDEGateway
	dto.Limits = p.Limits
	if svc := p.Service(store.ServiceNode); svc != nil && svc.Enabled && len(svc.Config) > 0 {
		var cfg runtime.NodeConfig
		if json.Unmarshal(svc.Config, &cfg) == nil && cfg.DevServer {
			dto.DevHostname = project.DevHostname(p.Slug, a.d.Projects.BaseDomain(r.Context()))
		}
	}
	return dto
}

func (a *API) project(r *http.Request, v project.View) projectDTO {
	return a.withHostnames(r, toProject(v), v.Project)
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	views, err := a.d.Projects.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out := make([]projectDTO, 0, len(views))
	for _, v := range views {
		if p.TokenName != "" && !p.CanAccessProject(v.Project.ID) {
			continue
		}
		out = append(out, a.project(r, v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// listOperations returns the running and recently finished project operations, so the
// UI can show what Envoryx is doing (image pulls, container recreation, backups).
func (a *API) listOperations(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	out := []project.Operation{}
	for _, op := range a.d.Projects.Operations() {
		if p.TokenName != "" && (op.ProjectID == "" && p.Restricted() || op.ProjectID != "" && !p.CanAccessProject(op.ProjectID)) {
			continue
		}
		out = append(out, op)
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.UseManifest && req.Git != nil && strings.TrimSpace(req.Git.URL) != "" {
		view, plan, err := a.d.Projects.CreateFromRepository(r.Context(), req.toDomain())
		a.invalidateProxy()
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"project": a.project(r, view), "manifest": plan})
		return
	}
	view, err := a.d.Projects.Create(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusCreated, map[string]any{"project": a.project(r, view)})
}

// duplicateProject copies a project: POST /projects/{id}/duplicate. The copy is a new
// project, so a token confined to particular projects may not make one.
func (a *API) duplicateProject(w http.ResponseWriter, r *http.Request) {
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" && p.Restricted() {
		writeError(w, r, fmt.Errorf("%w: this token is limited to particular projects and cannot create new ones", auth.ErrForbidden))
		return
	}
	var req duplicateProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.Duplicate(r.Context(), r.PathValue("id"), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusCreated, map[string]any{"project": a.project(r, view)})
}

// renameProject renames a project: POST /projects/{id}/rename. The answer carries the
// project plus what moved with it, so the UI can name the new database and bucket.
func (a *API) renameProject(w http.ResponseWriter, r *http.Request) {
	var req renameProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := a.d.Projects.Rename(r.Context(), r.PathValue("id"), project.RenameRequest{
		Name: req.Name, Path: req.Path, Confirm: req.Confirm, KeepDataNames: req.KeepDataNames,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{
		"project": a.project(r, res.View),
		"renamed": map[string]any{
			"from": res.From, "to": res.To, "path": res.Path,
			"database": res.Database, "username": res.Username, "bucket": res.Bucket,
		},
	})
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
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var req updateProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	upd := project.UpdateRequest{Name: req.Name, Docroot: req.Docroot}
	if req.Web != nil {
		upd.Web = &project.WebRequest{Type: req.Web.Type, Version: req.Web.Version, SPAFallback: req.Web.SPAFallback}
	}
	if req.PHP != nil {
		upd.PHP = &project.PHPUpdate{Enabled: req.PHP.Enabled == nil || *req.PHP.Enabled, Version: req.PHP.Version, Config: req.PHP.Config}
	}
	upd.IDEGateway = req.IDEGateway
	if req.Node != nil {
		upd.Node = &project.NodeUpdate{Enabled: req.Node.Enabled, Version: req.Node.Version, Config: req.Node.config()}
	}
	if req.Python != nil {
		upd.Python = &project.PythonUpdate{Enabled: req.Python.Enabled, Version: req.Python.Version, Config: req.Python.config()}
	}
	if req.Redis != nil {
		upd.Redis = &project.ExtraUpdate{Enabled: req.Redis.Enabled, Version: req.Redis.Version, ExposePort: req.Redis.ExposePort, RemoveData: req.Redis.RemoveData}
	}
	if req.Storage != nil {
		upd.Storage = &project.StorageUpdate{Enabled: req.Storage.Enabled, Version: req.Storage.Version, PublicRead: req.Storage.PublicRead, RemoveData: req.Storage.RemoveData}
	}
	if req.Mailpit != nil {
		upd.Mailpit = &project.ExtraUpdate{Enabled: req.Mailpit.Enabled, Version: req.Mailpit.Version}
	}
	if req.Memcached != nil {
		upd.Memcached = &project.ExtraUpdate{Enabled: req.Memcached.Enabled, Version: req.Memcached.Version, ExposePort: req.Memcached.ExposePort}
	}
	if req.RabbitMQ != nil {
		upd.RabbitMQ = &project.ExtraUpdate{Enabled: req.RabbitMQ.Enabled, Version: req.RabbitMQ.Version, ExposePort: req.RabbitMQ.ExposePort, RemoveData: req.RabbitMQ.RemoveData}
	}
	if req.Meilisearch != nil {
		upd.Meilisearch = &project.ExtraUpdate{Enabled: req.Meilisearch.Enabled, Version: req.Meilisearch.Version, RemoveData: req.Meilisearch.RemoveData}
	}
	if req.Typesense != nil {
		upd.Typesense = &project.ExtraUpdate{Enabled: req.Typesense.Enabled, Version: req.Typesense.Version, ExposePort: req.Typesense.ExposePort, RemoveData: req.Typesense.RemoveData}
	}
	if req.OpenSearch != nil {
		upd.OpenSearch = &project.ExtraUpdate{Enabled: req.OpenSearch.Enabled, Version: req.OpenSearch.Version, ExposePort: req.OpenSearch.ExposePort, RemoveData: req.OpenSearch.RemoveData, Dashboards: req.OpenSearch.Dashboards}
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
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
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
	a.invalidateProxy()
	w.WriteHeader(http.StatusNoContent)
}

// useImage rolls a project back to the previous image of one reference or returns it to
// the current one: POST /projects/{id}/images {"image": "<ref>", "use": "previous"|"latest"}.
func (a *API) useImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
		Use   string `json:"use"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Image == "" || len(req.Image) > 256 {
		writeError(w, r, fmt.Errorf("%w: image reference required", validate.ErrInvalid))
		return
	}
	view, err := a.d.Projects.UseImage(r.Context(), r.PathValue("id"), req.Image, project.ImageChoice(req.Use))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
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
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
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

// setLimits changes a project's resource limits: PUT /projects/{id}/limits. Running
// containers get them right away.
func (a *API) setLimits(w http.ResponseWriter, r *http.Request) {
	var req store.ResourceLimits
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.SetLimits(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
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
	// Each container with the limits it runs under, so usage can be shown against them.
	type containerDTO struct {
		stats.ContainerStats
		Group    string  `json:"group"`
		CPULimit float64 `json:"cpuLimit"` // cores, 0 = none
		MemLimit int64   `json:"memLimit"` // bytes, 0 = none
	}
	containers := []containerDTO{}
	for _, c := range usage.PerContainer {
		kind := store.ServiceKind(c.Service)
		set := view.Project.Limits.App
		if project.LimitGroup(kind) == "services" {
			set = view.Project.Limits.Services
		}
		containers = append(containers, containerDTO{ContainerStats: c, Group: project.LimitGroup(kind), CPULimit: set.CPUs, MemLimit: int64(set.MemoryMB) << 20})
	}
	usage.PerContainer = nil
	body := map[string]any{"stats": usage, "containers": containers, "limits": view.Project.Limits, "sampledAt": summary.SampledAt}
	if info, err := a.d.Engine.Ping(r.Context()); err == nil {
		body["host"] = map[string]any{"cpus": info.NCPU, "memory": info.MemTotal}
	}
	writeJSON(w, http.StatusOK, body)
}

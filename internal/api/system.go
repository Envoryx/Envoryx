package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/sshd"
	"github.com/envoryx/envoryx/internal/validate"

	"golang.org/x/crypto/ssh"
)

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	dockerOK := false
	if _, err := a.d.Engine.Ping(ctx); err == nil {
		dockerOK = true
	}
	dbOK := a.d.Store.DB().PingContext(ctx) == nil
	status := http.StatusOK
	state := "ok"
	if !dbOK {
		status, state = http.StatusServiceUnavailable, "degraded"
	}
	writeJSON(w, status, map[string]any{
		"status":   state,
		"version":  a.d.Version,
		"docker":   dockerOK,
		"database": dbOK,
		"uptime":   int(time.Since(a.d.StartedAt).Seconds()),
	})
}

type dockerInfoDTO struct {
	Connected     bool   `json:"connected"`
	Error         string `json:"error,omitempty"`
	APIVersion    string `json:"apiVersion,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	OS            string `json:"os,omitempty"`
	Architecture  string `json:"architecture,omitempty"`
	Containers    int    `json:"containers"`
	Running       int    `json:"running"`
	NCPU          int    `json:"ncpu"`
	MemTotal      int64  `json:"memTotal"`
}

func (a *API) dockerInfo(ctx context.Context) dockerInfoDTO {
	info, err := a.d.Engine.Ping(ctx)
	if err != nil {
		return dockerInfoDTO{Connected: false, Error: err.Error()}
	}
	return dockerInfoDTO{
		Connected: true, APIVersion: info.APIVersion, ServerVersion: info.ServerVersion, OS: info.OS,
		Architecture: info.Architecture, Containers: info.Containers, Running: info.Running, NCPU: info.NCPU, MemTotal: info.MemTotal,
	}
}

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	views, err := a.d.Projects.List(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	running, stopped, attention := 0, 0, 0
	projects := make([]projectDTO, 0, len(views))
	for _, v := range views {
		switch v.Status.State {
		case project.StateRunning:
			running++
		case project.StateStopped, project.StateMissing:
			stopped++
		default:
			attention++
		}
		projects = append(projects, toProject(v))
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].UpdatedAt.After(projects[j].UpdatedAt) })
	if len(projects) > 6 {
		projects = projects[:6]
	}

	var statsOut any
	if s, err := a.d.Stats.Summary(ctx); err == nil {
		statsOut = s
	}
	report := a.d.Projects.LastReport()
	writeJSON(w, http.StatusOK, map[string]any{
		"projects":   map[string]int{"total": len(views), "running": running, "stopped": stopped, "attention": attention},
		"docker":     a.dockerInfo(ctx),
		"stats":      statsOut,
		"recent":     projects,
		"issues":     report.Issues,
		"orphans":    len(report.Orphans),
		"hostPath":   a.d.HostPath.Status(),
		"version":    a.d.Version,
		"publicHost": a.publicHost(ctx),
		"baseDomain": a.d.Projects.BaseDomain(ctx),
		"proxy":      a.proxyDTO(),
	})
}

func (a *API) runtimes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"runtimes":      a.d.Catalog.All(),
		"templates":     project.Templates(),
		"phpExtensions": runtime.PHPExtensions(),
		"phpDefaults":   runtime.DefaultPHPConfig(),
	})
}

type containerDTO struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	State       string            `json:"state"`
	Status      string            `json:"status"`
	Created     time.Time         `json:"created"`
	Managed     bool              `json:"managed"`
	ProjectID   string            `json:"projectId,omitempty"`
	ProjectName string            `json:"projectName,omitempty"`
	Service     string            `json:"service,omitempty"`
	Ports       []portDTO         `json:"ports"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func toContainerDTO(c docker.Container) containerDTO {
	dto := containerDTO{
		ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status, Created: c.Created, Managed: c.Managed,
		Ports: toPorts(c.Ports),
	}
	if c.Managed {
		dto.ProjectID = c.ProjectID()
		dto.ProjectName = c.Labels[docker.LabelProjectName]
		dto.Service = c.Service()
		dto.Labels = c.Labels
	}
	return dto
}

// dockerOverview lists managed resources in full and foreign containers read-only with
// minimal information (name, image, state) for diagnostics.
func (a *API) dockerOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := a.dockerInfo(ctx)
	out := map[string]any{
		"info":       info,
		"containers": []containerDTO{},
		"foreign":    []containerDTO{},
		"networks":   []docker.Network{},
		"volumes":    []docker.Volume{},
		"orphans":    a.d.Projects.LastReport().Orphans,
		"hostPath":   a.d.HostPath.Status(),
	}
	if !info.Connected {
		writeJSON(w, http.StatusOK, out)
		return
	}
	containers, err := a.d.Engine.ListContainers(ctx, false, "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	managed, foreign := []containerDTO{}, []containerDTO{}
	for _, c := range containers {
		dto := toContainerDTO(c)
		if c.Managed {
			managed = append(managed, dto)
		} else {
			foreign = append(foreign, dto)
		}
	}
	networks, err := a.d.Engine.ListNetworks(ctx, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	volumes, err := a.d.Engine.ListVolumes(ctx, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if networks == nil {
		networks = []docker.Network{}
	}
	if volumes == nil {
		volumes = []docker.Volume{}
	}
	out["containers"], out["foreign"], out["networks"], out["volumes"] = managed, foreign, networks, volumes
	writeJSON(w, http.StatusOK, out)
}

// SettingPublicHost is the settings-table key for the project link host.
const SettingPublicHost = "public_host"

// publicHost returns the configured host for project links: the stored setting wins, then
// the environment, then "" (browser address bar).
func (a *API) publicHost(ctx context.Context) string {
	if v, err := a.d.Store.Settings.Get(ctx, SettingPublicHost); err == nil {
		return v
	}
	return a.d.Config.PublicHost
}

type updateSettingsRequest struct {
	PublicHost *string `json:"publicHost"`
	BaseDomain *string `json:"baseDomain"`
	ForceHTTPS *bool   `json:"forceHttps"`
	// XdebugClientHost is the developer machine Xdebug connects back to.
	XdebugClientHost *string `json:"xdebugClientHost"`
	// SSHAuthorizedKeys replaces the public keys accepted by the SSH server.
	SSHAuthorizedKeys *string `json:"sshAuthorizedKeys"`
}

func (a *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	changes := map[string]any{}
	if req.PublicHost != nil {
		host := strings.TrimSpace(*req.PublicHost)
		if host != "" && !config.ValidHost(host) {
			writeError(w, r, fmt.Errorf("%w: host must be a host name or IP address without scheme or port", validate.ErrInvalid))
			return
		}
		if err := a.d.Store.Settings.Set(r.Context(), SettingPublicHost, host); err != nil {
			writeError(w, r, err)
			return
		}
		changes["publicHost"] = host
	}
	if req.BaseDomain != nil {
		if err := a.d.Projects.SetBaseDomain(r.Context(), *req.BaseDomain); err != nil {
			writeError(w, r, err)
			return
		}
		a.invalidateProxy()
	}
	if req.SSHAuthorizedKeys != nil {
		if err := validateAuthorizedKeys(*req.SSHAuthorizedKeys); err != nil {
			writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", err.Error()))
			return
		}
		if err := a.d.Store.Settings.Set(r.Context(), sshd.SettingAuthorizedKeys, strings.TrimSpace(*req.SSHAuthorizedKeys)); err != nil {
			writeError(w, r, err)
			return
		}
		changes["sshAuthorizedKeys"] = strings.Count(strings.TrimSpace(*req.SSHAuthorizedKeys), "\n") + 1
	}
	if req.XdebugClientHost != nil {
		if err := a.d.Projects.SetXdebugClientHost(r.Context(), *req.XdebugClientHost); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if req.ForceHTTPS != nil {
		if err := a.d.Projects.SetForceHTTPS(r.Context(), *req.ForceHTTPS); err != nil {
			writeError(w, r, err)
			return
		}
		a.invalidateProxy()
	}
	if len(changes) > 0 {
		a.d.Audit.Log(r.Context(), audit.ActionSettingsChanged, "settings", "", changes)
	}
	a.settings(w, r)
}

func (a *API) unusedImages(w http.ResponseWriter, r *http.Request) {
	images, err := a.d.Projects.UnusedImages(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": images})
}

func (a *API) pruneImages(w http.ResponseWriter, r *http.Request) {
	res, err := a.d.Projects.PruneImages(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (a *API) settings(w http.ResponseWriter, r *http.Request) {
	c := a.d.Config
	schema, _ := db.SchemaVersion(r.Context(), a.d.Store.DB())
	writeJSON(w, http.StatusOK, map[string]any{
		"publicHost":        a.publicHost(r.Context()),
		"baseDomain":        a.d.Projects.BaseDomain(r.Context()),
		"forceHttps":        a.d.Projects.ForceHTTPS(r.Context()),
		"xdebugClientHost":  a.d.Projects.XdebugClientHost(r.Context()),
		"sshAuthorizedKeys": a.setting(r.Context(), sshd.SettingAuthorizedKeys),
		"ssh":               a.sshDTO(),
		"proxy":             a.proxyDTO(),
		"version":           a.d.Version,
		"schemaVersion":     schema,
		"configDir":         c.ConfigDir,
		"projectsDir":       c.ProjectsDir,
		"hostPath":          a.d.HostPath.Status(),
		"portRange":         map[string]int{"start": c.PortRangeStart, "end": c.PortRangeEnd},
		"puid":              c.PUID,
		"pgid":              c.PGID,
		"dockerHost":        c.DockerHost,
		"session":           map[string]string{"idleTimeout": c.SessionIdleTimeout.String(), "absoluteTimeout": c.SessionAbsoluteTimeout.String()},
		"secureCookies":     c.SecureCookies,
		"warnings":          append([]string{}, a.d.Warnings...),
	})
}

func (a *API) auditLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := a.d.Store.Audit.Recent(r.Context(), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type entryDTO struct {
		ID         string    `json:"id"`
		CreatedAt  time.Time `json:"createdAt"`
		Username   string    `json:"username"`
		Action     string    `json:"action"`
		TargetType string    `json:"targetType"`
		TargetID   string    `json:"targetId"`
		Details    any       `json:"details"`
		IP         string    `json:"ip"`
	}
	out := make([]entryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryDTO{ID: e.ID, CreatedAt: e.CreatedAt, Username: e.Username, Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID, Details: e.Details, IP: e.IP})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (a *API) reconcileReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"report": a.d.Projects.LastReport()})
}

func (a *API) reconcileNow(w http.ResponseWriter, r *http.Request) {
	report := a.d.Projects.Reconcile(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

func (a *API) setting(ctx context.Context, key string) string {
	v, _ := a.d.Store.Settings.Get(ctx, key)
	return v
}

func (a *API) sshDTO() SSHInfo {
	if a.d.SSH == nil {
		return SSHInfo{}
	}
	return *a.d.SSH
}

// validateAuthorizedKeys checks every non-empty, non-comment line parses as a public key.
func validateAuthorizedKeys(text string) error {
	if len(text) > 64<<10 {
		return errors.New("too many keys")
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
			return fmt.Errorf("line %d is not a valid public key", i+1)
		}
	}
	return nil
}

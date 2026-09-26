package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// The project manifest (envoryx.yml, see internal/manifest) is the desired state kept in
// the repository. Both directions go through exportState: the current project is
// exported as it is stored, the manifest is first built into a project the way Create
// would build it (buildProject, buildWorker, buildCronJob – versions resolved, configs
// normalised, everything validated) and then exported the same way. Comparing the two
// exports section by section yields the plan, and a project created from a manifest is
// in sync with it by construction.

// ManifestChange is one difference between a project and its manifest.
type ManifestChange struct {
	// Section is docroot, web, php, node, python, database, redis, …, storage, env,
	// domain, worker or cron.
	Section string `json:"section"`
	// Item names the variable, host name, worker or cron job within the section.
	Item string `json:"item,omitempty"`
	// Action is add, change or remove.
	Action string `json:"action"`
	// From and To summarise the differing settings ("version: 8.3" → "version: 8.4").
	// Secret values are never shown.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Skipped says why the change is not made: "prune" (a removal needs --prune) or
	// "downgrade" (the data format does not go back).
	Skipped string `json:"skipped,omitempty"`
}

// ManifestPlan is what applying a manifest to a project changes.
type ManifestPlan struct {
	Changes []ManifestChange `json:"changes"`
	// MissingSecrets are variables the manifest declares under secrets that have no value
	// on the project (yet).
	MissingSecrets []string `json:"missingSecrets"`
	// InSync is true when nothing would change, skipped changes included.
	InSync bool `json:"inSync"`
}

// Pending reports whether applying would change anything.
func (p ManifestPlan) Pending() bool {
	for _, c := range p.Changes {
		if c.Skipped == "" {
			return true
		}
	}
	return false
}

// ManifestOptions control how a manifest is applied.
type ManifestOptions struct {
	// Prune removes services, variables, domains, workers and cron jobs the manifest no
	// longer has. Without it removals are listed as skipped.
	Prune bool
	// Secrets are values for variables declared under secrets.
	Secrets map[string]string
	// Start starts the project afterwards.
	Start bool
}

// ManifestResult is the outcome of applying or creating from a manifest.
type ManifestResult struct {
	Plan ManifestPlan
	View View
}

// ManifestCreateRequest creates a project from a manifest.
type ManifestCreateRequest struct {
	// Name overrides the manifest's name.
	Name string
	Path string
	// Git is the repository the server clones into the new project.
	Git     *GitRequest
	Secrets map[string]string
	Start   bool
}

// RepositoryManifest is the envoryx.yml found in a project's directory.
type RepositoryManifest struct {
	Present bool `json:"present"`
	// Error is set when the file is there but cannot be used.
	Error string `json:"error,omitempty"`
	// Plan compares the file with the project (nil unless the file is usable).
	Plan *ManifestPlan `json:"plan,omitempty"`
}

// ---- export -----------------------------------------------------------------

// ExportManifest describes a project as a manifest.
func (m *Manager) ExportManifest(ctx context.Context, id string) (manifest.Manifest, error) {
	p, domains, jobs, err := m.manifestState(ctx, id)
	if err != nil {
		return manifest.Manifest{}, err
	}
	return exportState(p, domains, jobs), nil
}

func (m *Manager) manifestState(ctx context.Context, id string) (store.Project, []store.Domain, []store.CronJob, error) {
	if err := validate.UUID(id); err != nil {
		return store.Project{}, nil, nil, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return store.Project{}, nil, nil, err
	}
	domains, err := m.store.Domains.ListByProject(ctx, id)
	if err != nil {
		return store.Project{}, nil, nil, err
	}
	jobs, err := m.store.CronJobs.ListByProject(ctx, id)
	if err != nil {
		return store.Project{}, nil, nil, err
	}
	return p, domains, jobs, nil
}

// exportState turns a stored project into its manifest. Host ports, credentials and the
// repository are left out: the server assigns the first two and the manifest lives in
// the third. Settings at their default are left out too, so the file stays short.
func exportState(p store.Project, domains []store.Domain, jobs []store.CronJob) manifest.Manifest {
	mf := manifest.Manifest{Version: manifest.CurrentVersion, Name: p.Name, Docroot: p.Docroot}
	if web := p.Service(store.ServiceWeb); web != nil {
		cfg, _ := webServiceConfig(*web)
		mf.Web = &manifest.Web{Server: web.Variant, Version: web.Version, SPAFallback: cfg.SPAFallback}
	}
	if svc := p.Service(store.ServicePHP); svc != nil {
		var cfg runtime.PHPConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		def := runtime.DefaultPHPConfig()
		php := &manifest.PHP{Version: svc.Version, Extensions: slices.Clone(cfg.Extensions), Xdebug: cfg.Xdebug}
		if php.Extensions == nil {
			php.Extensions = []string{}
		}
		if cfg.MemoryLimit != def.MemoryLimit {
			php.MemoryLimit = cfg.MemoryLimit
		}
		if cfg.UploadMaxFilesize != def.UploadMaxFilesize {
			php.UploadMaxFilesize = cfg.UploadMaxFilesize
		}
		if cfg.PostMaxSize != def.PostMaxSize {
			php.PostMaxSize = cfg.PostMaxSize
		}
		if cfg.MaxExecutionTime != def.MaxExecutionTime {
			php.MaxExecutionTime = cfg.MaxExecutionTime
		}
		if !cfg.DisplayErrors {
			off := false
			php.DisplayErrors = &off
		}
		if cfg.ErrorReporting != def.ErrorReporting {
			php.ErrorReporting = cfg.ErrorReporting
		}
		if cfg.XdebugMode != "" && cfg.XdebugMode != "always" {
			php.XdebugMode = cfg.XdebugMode
		}
		if cfg.XdebugIDEKey != "" && cfg.XdebugIDEKey != "PHPSTORM" {
			php.XdebugIDEKey = cfg.XdebugIDEKey
		}
		mf.PHP = php
	}
	if svc := p.Service(store.ServiceNode); svc != nil {
		var cfg runtime.NodeConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		mf.Node = &manifest.Node{
			Version: svc.Version, DevServer: cfg.DevServer, Mode: cfg.Mode, PackageManager: cfg.PackageManager,
			Script: cfg.Script, BuildScript: cfg.BuildScript, Port: cfg.Port, Preset: cfg.Preset,
			Inspect: cfg.Inspect, InspectPort: cfg.InspectPort,
		}
	}
	if svc := p.Service(store.ServicePython); svc != nil {
		var cfg runtime.PythonConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		mf.Python = &manifest.Python{
			Version: svc.Version, Server: cfg.Server, Mode: cfg.Mode, Preset: cfg.Preset, App: cfg.App,
			Port: cfg.Port, Debug: cfg.Debug, DebugPort: cfg.DebugPort,
		}
	}
	if svc := p.Service(store.ServiceGo); svc != nil {
		var cfg runtime.GoConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		mf.Go = &manifest.Go{
			Version: svc.Version, Server: cfg.Server, Mode: cfg.Mode, Package: cfg.Package,
			Port: cfg.Port, Debug: cfg.Debug, DebugPort: cfg.DebugPort,
		}
	}
	for _, svc := range p.Databases() {
		var cfg runtime.DatabaseConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		d := manifest.Database{Type: svc.Variant, Version: svc.Version, ExposePort: cfg.HostPort != 0}
		if cfg.External() {
			d.External = &manifest.External{Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, Database: cfg.Database}
		}
		if name := svc.Kind.DatabaseName(); name != "" {
			if mf.Databases == nil {
				mf.Databases = map[string]manifest.Database{}
			}
			mf.Databases[name] = d
		} else {
			mf.Database = &d
		}
	}
	for _, kind := range extraKinds {
		svc := p.Service(kind)
		if svc == nil {
			continue
		}
		var cfg runtime.ServiceConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		s := &manifest.Service{Version: svc.Version, ExposePort: cfg.HostPort != 0 && !extraAlwaysPublished(kind)}
		if kind == store.ServiceOpenSearch {
			s.Dashboards = p.Service(store.ServiceOpenSearchDashboards) != nil
		}
		s.GPU = cfg.GPU
		if cfg.External() {
			s.External = &manifest.External{Host: cfg.Host, Port: cfg.Port}
		}
		*manifestService(&mf, kind) = s
	}
	if svc := p.Service(store.ServiceStorage); svc != nil {
		var cfg runtime.StorageConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		st := &manifest.Storage{Version: svc.Version}
		if !cfg.PublicRead {
			off := false
			st.PublicRead = &off
		}
		mf.Storage = st
	}
	for _, d := range domains {
		mf.Domains = append(mf.Domains, d.Hostname)
	}
	sort.Strings(mf.Domains)
	for _, e := range p.Env {
		if e.IsSecret {
			mf.Secrets = append(mf.Secrets, e.Key)
			continue
		}
		if mf.Env == nil {
			mf.Env = map[string]string{}
		}
		mf.Env[e.Key] = e.Value
	}
	sort.Strings(mf.Secrets)
	for _, w := range p.Workers {
		mw := manifest.Worker{Name: w.Name, Preset: w.Preset}
		if len(w.Args) > 0 {
			mw.Arg = w.Args[0]
		}
		if !w.Enabled {
			off := false
			mw.Enabled = &off
		}
		mf.Workers = append(mf.Workers, mw)
	}
	if !p.Limits.IsZero() {
		set := func(l store.LimitSet) *manifest.LimitSet {
			if l.IsZero() {
				return nil
			}
			return &manifest.LimitSet{CPUs: l.CPUs, Memory: manifest.FormatMemory(l.MemoryMB)}
		}
		mf.Limits = &manifest.Limits{App: set(p.Limits.App), Services: set(p.Limits.Services), Pids: p.Limits.Pids}
	}
	mf.HealthCheck = exportHealthCheck(p.HealthCheck)
	for _, j := range jobs {
		mj := manifest.CronJob{Name: j.Name, Schedule: j.Schedule, Runtime: j.Runtime, Command: j.Command}
		if j.Timeout != cronDefaultTimeout {
			mj.Timeout = formatDuration(j.Timeout)
		}
		if !j.Enabled {
			off := false
			mj.Enabled = &off
		}
		mf.Cron = append(mf.Cron, mj)
	}
	return mf
}

// manifestService returns the manifest field of an auxiliary service kind.
func manifestService(mf *manifest.Manifest, kind store.ServiceKind) **manifest.Service {
	switch kind {
	case store.ServiceRedis:
		return &mf.Redis
	case store.ServiceMemcached:
		return &mf.Memcached
	case store.ServiceMailpit:
		return &mf.Mailpit
	case store.ServiceRabbitMQ:
		return &mf.RabbitMQ
	case store.ServiceMeilisearch:
		return &mf.Meilisearch
	case store.ServiceTypesense:
		return &mf.Typesense
	case store.ServiceOpenSearch:
		return &mf.OpenSearch
	case store.ServiceOllama:
		return &mf.Ollama
	}
	panic("manifestService: no manifest field for " + string(kind))
}

// formatDuration writes a timeout the way people type it: 90s, 5m, 2h, 1h30m.
func formatDuration(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	if s == "" {
		return "0s"
	}
	return s
}

// ---- manifest → desired state ------------------------------------------------

// manifestRequest turns a manifest into the create request that builds its services.
// Environment, domains, workers and cron jobs are not part of it.
// hasExternal reports whether a manifest connects anything to an external server.
func hasExternal(mf manifest.Manifest) bool {
	if (mf.Database != nil && mf.Database.External != nil) || (mf.Redis != nil && mf.Redis.External != nil) {
		return true
	}
	for _, d := range mf.Databases {
		if d.External != nil {
			return true
		}
	}
	return false
}

// manifestDBType maps what people write to the catalogue's key.
func manifestDBType(t string) string {
	if t == "postgres" {
		return "postgresql"
	}
	return t
}

func manifestRequest(mf manifest.Manifest, name string) CreateRequest {
	req := CreateRequest{Name: name, Docroot: mf.Docroot}
	if mf.Web != nil {
		req.Web = WebRequest{Type: mf.Web.Server, Version: mf.Web.Version}
		if mf.Web.SPAFallback {
			on := true
			req.Web.SPAFallback = &on
		}
	}
	if mf.PHP != nil {
		req.PHP = &PHPRequest{Version: mf.PHP.Version, Config: manifestPHPConfig(*mf.PHP)}
	}
	if mf.Node != nil {
		n := mf.Node
		req.Node = &NodeRequest{Version: n.Version, Config: runtime.NodeConfig{
			DevServer: n.DevServer, Mode: n.Mode, PackageManager: n.PackageManager, Script: n.Script,
			BuildScript: n.BuildScript, Port: n.Port, Preset: n.Preset, Inspect: n.Inspect, InspectPort: n.InspectPort,
		}}
	}
	if mf.Python != nil {
		py := mf.Python
		req.Python = &PythonRequest{Version: py.Version, Config: runtime.PythonConfig{
			Server: py.Server, Mode: py.Mode, Preset: py.Preset, App: py.App, Port: py.Port, Debug: py.Debug, DebugPort: py.DebugPort,
		}}
	}
	if mf.Go != nil {
		g := mf.Go
		req.Go = &GoRequest{Version: g.Version, Config: runtime.GoConfig{
			Server: g.Server, Mode: g.Mode, Package: g.Package, Port: g.Port, Debug: g.Debug, DebugPort: g.DebugPort,
		}}
	}
	// An external connection comes without its password, which the file never holds.
	external := func(e *manifest.External) *ExternalDatabase {
		if e == nil {
			return nil
		}
		return &ExternalDatabase{Host: e.Host, Port: e.Port, Username: e.Username, Database: e.Database}
	}
	if mf.Database != nil {
		req.Database = &DatabaseRequest{Type: manifestDBType(mf.Database.Type), Version: mf.Database.Version, ExposePort: mf.Database.ExposePort, External: external(mf.Database.External)}
	}
	for _, name := range slices.Sorted(maps.Keys(mf.Databases)) {
		d := mf.Databases[name]
		req.Databases = append(req.Databases, NamedDatabaseRequest{Name: name, DatabaseRequest: DatabaseRequest{Type: manifestDBType(d.Type), Version: d.Version, ExposePort: d.ExposePort, External: external(d.External)}})
	}
	extra := func(s *manifest.Service) *ExtraRequest {
		if s == nil {
			return nil
		}
		r := &ExtraRequest{Version: s.Version, ExposePort: s.ExposePort, Dashboards: s.Dashboards, GPU: s.GPU}
		if s.External != nil {
			r.External = &ExternalRedis{Host: s.External.Host, Port: s.External.Port}
		}
		return r
	}
	req.Redis, req.Memcached, req.Mailpit = extra(mf.Redis), extra(mf.Memcached), extra(mf.Mailpit)
	req.RabbitMQ, req.Meilisearch, req.Typesense = extra(mf.RabbitMQ), extra(mf.Meilisearch), extra(mf.Typesense)
	req.OpenSearch, req.Ollama = extra(mf.OpenSearch), extra(mf.Ollama)
	if mf.Storage != nil {
		pr := mf.Storage.IsPublicRead()
		req.Storage = &StorageRequest{Version: mf.Storage.Version, PublicRead: &pr}
	}
	req.Limits = manifestLimits(mf.Limits)
	req.HealthCheck = manifestHealthCheck(mf.HealthCheck)
	return req
}

// manifestHealthCheck converts the file's health check (checked by Validate).
func manifestHealthCheck(h *manifest.HealthCheck) store.HealthCheck {
	if h == nil {
		return store.HealthCheck{}
	}
	interval, timeout, _ := h.Seconds()
	return store.HealthCheck{Path: strings.TrimSpace(h.Path), Status: h.Status, IntervalSec: interval, TimeoutSec: timeout, Failures: h.Failures}
}

// exportHealthCheck writes a health check without its defaults (nil: none).
func exportHealthCheck(h store.HealthCheck) *manifest.HealthCheck {
	if n, err := normalizeHealthCheck(h); err == nil {
		h = n
	}
	if !h.Enabled() {
		return nil
	}
	return &manifest.HealthCheck{Path: h.Path, Status: h.Status, Interval: manifest.FormatSeconds(h.IntervalSec), Timeout: manifest.FormatSeconds(h.TimeoutSec), Failures: h.Failures}
}

// manifestLimits converts the file's limits (the sizes are checked by Validate).
func manifestLimits(l *manifest.Limits) store.ResourceLimits {
	if l == nil {
		return store.ResourceLimits{}
	}
	set := func(s *manifest.LimitSet) store.LimitSet {
		if s == nil {
			return store.LimitSet{}
		}
		mb, _ := s.MemoryMB()
		return store.LimitSet{CPUs: s.CPUs, MemoryMB: mb}
	}
	return store.ResourceLimits{App: set(l.App), Services: set(l.Services), Pids: l.Pids}
}

func manifestPHPConfig(p manifest.PHP) runtime.PHPConfig {
	cfg := runtime.PHPConfig{
		MemoryLimit: p.MemoryLimit, UploadMaxFilesize: p.UploadMaxFilesize, PostMaxSize: p.PostMaxSize,
		MaxExecutionTime: p.MaxExecutionTime, DisplayErrors: p.DisplayErrors == nil || *p.DisplayErrors,
		ErrorReporting: p.ErrorReporting, Extensions: slices.Clone(p.Extensions),
		Xdebug: p.Xdebug, XdebugMode: p.XdebugMode, XdebugIDEKey: p.XdebugIDEKey,
	}
	return cfg
}

// desiredState builds what the manifest describes the way Create would, without side
// effects. Published ports are marked with a placeholder so the export says exposePort.
func (m *Manager) desiredState(mf manifest.Manifest, name string) (store.Project, []store.CronJob, error) {
	p, err := m.buildProject(manifestRequest(mf, name))
	if err != nil {
		return store.Project{}, nil, err
	}
	expose := map[store.ServiceKind]bool{}
	if mf.Database != nil && mf.Database.ExposePort {
		expose[store.ServiceDatabase] = true
	}
	for name, d := range mf.Databases {
		if d.ExposePort {
			expose[store.DatabaseKind(name)] = true
		}
	}
	for _, kind := range extraKinds {
		if s := *manifestService(&mf, kind); s != nil && s.ExposePort {
			expose[kind] = true
		}
	}
	for i := range p.Services {
		if expose[p.Services[i].Kind] {
			if err := setHostPort(&p.Services[i], 1); err != nil {
				return store.Project{}, nil, err
			}
		}
	}
	for _, mw := range mf.Workers {
		w, err := buildWorker("", WorkerRequest{Name: mw.Name, Preset: mw.Preset, Arg: mw.Arg, Enabled: mw.IsEnabled()})
		if err != nil {
			return store.Project{}, nil, fmt.Errorf("worker %s: %w", mw.Name, err)
		}
		p.Workers = append(p.Workers, w)
	}
	var jobs []store.CronJob
	for _, mj := range mf.Cron {
		req, err := cronRequest(mj, p)
		if err != nil {
			return store.Project{}, nil, err
		}
		j, err := buildCronJob("", req)
		if err != nil {
			return store.Project{}, nil, fmt.Errorf("cron job %s: %w", mj.Name, err)
		}
		jobs = append(jobs, j)
	}
	return p, jobs, nil
}

// cronRequest maps a manifest cron job; without a runtime it runs in the application
// container (PHP when there is none, which the runtime check then reports).
func cronRequest(mj manifest.CronJob, p store.Project) (CronJobRequest, error) {
	timeout, err := mj.TimeoutDuration()
	if err != nil {
		return CronJobRequest{}, err
	}
	rt := strings.TrimSpace(mj.Runtime)
	if rt == "" {
		rt = WorkerRuntimePHP
		if kind, ok := AppKind(p); ok {
			rt = string(kind)
		}
	}
	return CronJobRequest{Name: mj.Name, Runtime: rt, Schedule: mj.Schedule, Command: mj.Command, Timeout: timeout, Enabled: mj.IsEnabled()}, nil
}

// ---- plan -------------------------------------------------------------------

// manifestOps is the plan as operations.
type manifestOps struct {
	update UpdateRequest
	// databaseAdd is applied in a second update after the old database is removed
	// (a change of the database type); databasesAdd the same for additional databases.
	databaseAdd   *DatabaseUpdate
	databasesAdd  map[string]DatabaseUpdate
	addDomains    []string
	removeDomains []store.Domain
	addWorkers    []WorkerRequest
	updateWorkers map[string]WorkerRequest // by worker id
	removeWorkers []store.Worker
	addCron       []CronJobRequest
	updateCron    map[string]CronJobRequest // by job id
	removeCron    []store.CronJob
	// limits are set through SetLimits (nil = unchanged).
	limits *store.ResourceLimits
	// health is set through SetHealthCheck (nil = unchanged).
	health *store.HealthCheck
}

func (o manifestOps) hasUpdate() bool { return !reflect.DeepEqual(o.update, UpdateRequest{}) }

// PlanManifest compares a manifest with a project.
func (m *Manager) PlanManifest(ctx context.Context, id string, mf manifest.Manifest, opts ManifestOptions) (ManifestPlan, error) {
	plan, _, err := m.planManifest(ctx, id, mf, opts)
	return plan, err
}

func (m *Manager) planManifest(ctx context.Context, id string, mf manifest.Manifest, opts ManifestOptions) (ManifestPlan, manifestOps, error) {
	cur, domains, jobs, err := m.manifestState(ctx, id)
	if err != nil {
		return ManifestPlan{}, manifestOps{}, err
	}
	want, wantJobs, err := m.desiredState(mf, cur.Name)
	if err != nil {
		return ManifestPlan{}, manifestOps{}, err
	}
	for k := range opts.Secrets {
		if !slices.Contains(mf.Secrets, k) {
			return ManifestPlan{}, manifestOps{}, fmt.Errorf("%w: %s is not declared under secrets in %s", validate.ErrInvalid, k, manifest.FileName)
		}
	}
	have := exportState(cur, domains, jobs)
	// Domains are compared as the manifest names them; desiredState knows none.
	wantMf := exportState(want, nil, wantJobs)
	for _, h := range mf.Domains {
		wantMf.Domains = append(wantMf.Domains, validate.NormalizeHostname(h))
	}
	sort.Strings(wantMf.Domains)

	var (
		plan ManifestPlan
		ops  = manifestOps{updateWorkers: map[string]WorkerRequest{}, updateCron: map[string]CronJobRequest{}}
	)
	add := func(c ManifestChange) { plan.Changes = append(plan.Changes, c) }
	removal := func(c ManifestChange) bool {
		if !opts.Prune {
			c.Skipped = "prune"
		}
		add(c)
		return opts.Prune
	}

	if have.Docroot != wantMf.Docroot {
		d := wantMf.Docroot
		ops.update.Docroot = &d
		add(ManifestChange{Section: "docroot", Action: "change", From: have.Docroot, To: d})
	}
	if !reflect.DeepEqual(have.Web, wantMf.Web) && wantMf.Web != nil {
		from, to := diffSections(have.Web, wantMf.Web)
		add(ManifestChange{Section: "web", Action: "change", From: from, To: to})
		ops.update.Web = &WebRequest{Type: wantMf.Web.Server, Version: wantMf.Web.Version}
		// The SPA option is refused on any project with PHP, even when it is switched off.
		if have.PHP == nil && wantMf.PHP == nil {
			spa := wantMf.Web.SPAFallback
			ops.update.Web.SPAFallback = &spa
		}
	}

	// PHP, Node and Python: add, change or (with prune) remove the runtime.
	if c, ok := sectionChange("php", have.PHP, wantMf.PHP); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.update.PHP = &PHPUpdate{Enabled: false}
			}
		} else {
			add(c)
			cfg := manifestPHPConfig(*wantMf.PHP)
			if want.Service(store.ServicePHP) != nil {
				_ = json.Unmarshal(want.Service(store.ServicePHP).Config, &cfg)
			}
			if svc := cur.Service(store.ServicePHP); svc != nil {
				var old runtime.PHPConfig
				_ = json.Unmarshal(svc.Config, &old)
				cfg.XdebugClientHost = old.XdebugClientHost // this server's, not the repository's
			}
			ops.update.PHP = &PHPUpdate{Enabled: true, Version: wantMf.PHP.Version, Config: cfg}
		}
	}
	if c, ok := sectionChange("node", have.Node, wantMf.Node); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.update.Node = &NodeUpdate{Enabled: false}
			}
		} else {
			add(c)
			var cfg runtime.NodeConfig
			_ = json.Unmarshal(want.Service(store.ServiceNode).Config, &cfg)
			ops.update.Node = &NodeUpdate{Enabled: true, Version: wantMf.Node.Version, Config: cfg}
		}
	}
	if c, ok := sectionChange("python", have.Python, wantMf.Python); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.update.Python = &PythonUpdate{Enabled: false}
			}
		} else {
			add(c)
			var cfg runtime.PythonConfig
			_ = json.Unmarshal(want.Service(store.ServicePython).Config, &cfg)
			ops.update.Python = &PythonUpdate{Enabled: true, Version: wantMf.Python.Version, Config: cfg}
		}
	}
	if c, ok := sectionChange("go", have.Go, wantMf.Go); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.update.Go = &GoUpdate{Enabled: false}
			}
		} else {
			add(c)
			var cfg runtime.GoConfig
			_ = json.Unmarshal(want.Service(store.ServiceGo).Config, &cfg)
			ops.update.Go = &GoUpdate{Enabled: true, Version: wantMf.Go.Version, Config: cfg}
		}
	}

	// The databases: another type means a new, empty database, so it counts as removal.
	// update and later are where the change goes (the primary's fields, or an additional
	// database's entry).
	diffDatabase := func(section string, h, w *manifest.Database, update func(DatabaseUpdate), later func(DatabaseUpdate)) {
		c, ok := sectionChange(section, h, w)
		if !ok {
			return
		}
		switch {
		case c.Action == "remove":
			if removal(c) {
				update(DatabaseUpdate{Enabled: false, RemoveData: true})
			}
		case (c.Action == "add" && w.External != nil) || (c.Action == "change" && (h.External == nil) != (w.External == nil)) || (c.Action == "change" && w.External != nil && h.Type != w.Type):
			// A new external connection needs its password, which the file never has.
			c.Skipped = "external"
			add(c)
		case c.Action == "change" && w.External != nil:
			add(c)
			e := w.External
			update(DatabaseUpdate{Enabled: true, Type: w.Type, Version: w.Version, External: &ExternalDatabase{Host: e.Host, Port: e.Port, Username: e.Username, Database: e.Database}})
		case c.Action == "add":
			add(c)
			update(DatabaseUpdate{Enabled: true, Type: w.Type, Version: w.Version, ExposePort: w.ExposePort})
		case h.Type != w.Type:
			if removal(c) {
				update(DatabaseUpdate{Enabled: false, RemoveData: true})
				later(DatabaseUpdate{Enabled: true, Type: w.Type, Version: w.Version, ExposePort: w.ExposePort})
			}
		case runtime.CompareVersions(w.Version, h.Version) < 0:
			c.Skipped = "downgrade"
			add(c)
		default:
			add(c)
			update(DatabaseUpdate{Enabled: true, Type: w.Type, Version: w.Version, ExposePort: w.ExposePort})
		}
	}
	diffDatabase("database", have.Database, wantMf.Database,
		func(u DatabaseUpdate) { ops.update.Database = &u },
		func(u DatabaseUpdate) { ops.databaseAdd = &u })
	names := map[string]bool{}
	for n := range have.Databases {
		names[n] = true
	}
	for n := range wantMf.Databases {
		names[n] = true
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		var h, w *manifest.Database
		if d, ok := have.Databases[name]; ok {
			h = &d
		}
		if d, ok := wantMf.Databases[name]; ok {
			w = &d
		}
		diffDatabase("databases."+name, h, w,
			func(u DatabaseUpdate) {
				if ops.update.Databases == nil {
					ops.update.Databases = map[string]DatabaseUpdate{}
				}
				ops.update.Databases[name] = u
			},
			func(u DatabaseUpdate) {
				if ops.databasesAdd == nil {
					ops.databasesAdd = map[string]DatabaseUpdate{}
				}
				ops.databasesAdd[name] = u
			})
	}

	for _, kind := range extraKinds {
		h, w := *manifestService(&have, kind), *manifestService(&wantMf, kind)
		c, ok := sectionChange(string(kind), h, w)
		if !ok {
			continue
		}
		var upd *ExtraUpdate
		switch {
		case c.Action == "remove":
			if !removal(c) {
				continue
			}
			upd = &ExtraUpdate{Enabled: false, RemoveData: true}
		case (c.Action == "add" && w.External != nil) || (c.Action == "change" && (h.External == nil) != (w.External == nil)):
			c.Skipped = "external" // the password is not in the file
			add(c)
			continue
		case w.External != nil:
			add(c)
			upd = &ExtraUpdate{Enabled: true, Version: w.Version, External: &ExternalRedis{Host: w.External.Host, Port: w.External.Port}}
		default:
			add(c)
			upd = &ExtraUpdate{Enabled: true, Version: w.Version, ExposePort: w.ExposePort}
			if kind == store.ServiceOpenSearch {
				d := w.Dashboards
				upd.Dashboards = &d
			}
			if kind == store.ServiceOllama {
				g := w.GPU
				upd.GPU = &g
			}
		}
		switch kind {
		case store.ServiceRedis:
			ops.update.Redis = upd
		case store.ServiceMemcached:
			ops.update.Memcached = upd
		case store.ServiceMailpit:
			ops.update.Mailpit = upd
		case store.ServiceRabbitMQ:
			ops.update.RabbitMQ = upd
		case store.ServiceMeilisearch:
			ops.update.Meilisearch = upd
		case store.ServiceTypesense:
			ops.update.Typesense = upd
		case store.ServiceOpenSearch:
			ops.update.OpenSearch = upd
		case store.ServiceOllama:
			ops.update.Ollama = upd
		}
	}
	if c, ok := sectionChange("storage", have.Storage, wantMf.Storage); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.update.Storage = &StorageUpdate{Enabled: false, RemoveData: true}
			}
		} else {
			add(c)
			pr := wantMf.Storage.IsPublicRead()
			ops.update.Storage = &StorageUpdate{Enabled: true, Version: wantMf.Storage.Version, PublicRead: &pr}
		}
	}

	if c, ok := sectionChange("limits", have.Limits, wantMf.Limits); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.limits = &store.ResourceLimits{}
			}
		} else {
			add(c)
			l := want.Limits
			ops.limits = &l
		}
	}

	// Compared in the form the project exports, so "interval: 30s" (the default) is no
	// change.
	if c, ok := sectionChange("healthcheck", have.HealthCheck, exportHealthCheck(want.HealthCheck)); ok {
		if c.Action == "remove" {
			if removal(c) {
				ops.health = &store.HealthCheck{}
			}
		} else {
			add(c)
			h := want.HealthCheck
			ops.health = &h
		}
	}

	// Environment: plain values come from the manifest, secrets keep what the project
	// has unless a value was given; variables the manifest does not know stay unless
	// pruned.
	env, envChanged := m.planEnv(cur, mf, opts, add, removal, &plan)
	if envChanged {
		ops.update.Env = &env
	}

	// Domains.
	haveHosts := map[string]store.Domain{}
	for _, d := range domains {
		haveHosts[d.Hostname] = d
	}
	for _, h := range wantMf.Domains {
		if _, ok := haveHosts[h]; ok {
			delete(haveHosts, h)
			continue
		}
		add(ManifestChange{Section: "domain", Item: h, Action: "add"})
		ops.addDomains = append(ops.addDomains, h)
	}
	for _, h := range sortedKeys(haveHosts) {
		if removal(ManifestChange{Section: "domain", Item: h, Action: "remove"}) {
			ops.removeDomains = append(ops.removeDomains, haveHosts[h])
		}
	}

	// Workers and cron jobs, matched by name.
	haveWorkers := map[string]store.Worker{}
	for _, w := range cur.Workers {
		haveWorkers[w.Name] = w
	}
	for i, w := range want.Workers {
		req := WorkerRequest{Name: w.Name, Preset: w.Preset, Enabled: w.Enabled}
		if len(w.Args) > 0 {
			req.Arg = w.Args[0]
		}
		old, ok := haveWorkers[w.Name]
		delete(haveWorkers, w.Name)
		if !ok {
			add(ManifestChange{Section: "worker", Item: w.Name, Action: "add", To: describe(wantMf.Workers[i])})
			ops.addWorkers = append(ops.addWorkers, req)
			continue
		}
		if old.Preset != w.Preset || !slices.Equal(old.Args, w.Args) || old.Enabled != w.Enabled {
			from, to := diffSections(workerManifest(old), wantMf.Workers[i])
			add(ManifestChange{Section: "worker", Item: w.Name, Action: "change", From: from, To: to})
			ops.updateWorkers[old.ID] = req
		}
	}
	for _, name := range sortedKeys(haveWorkers) {
		if removal(ManifestChange{Section: "worker", Item: name, Action: "remove"}) {
			ops.removeWorkers = append(ops.removeWorkers, haveWorkers[name])
		}
	}
	haveJobs := map[string]store.CronJob{}
	for _, j := range jobs {
		haveJobs[j.Name] = j
	}
	for i, j := range wantJobs {
		req := CronJobRequest{Name: j.Name, Runtime: j.Runtime, Schedule: j.Schedule, Command: j.Command, Timeout: j.Timeout, Enabled: j.Enabled}
		old, ok := haveJobs[j.Name]
		delete(haveJobs, j.Name)
		if !ok {
			add(ManifestChange{Section: "cron", Item: j.Name, Action: "add", To: describe(wantMf.Cron[i])})
			ops.addCron = append(ops.addCron, req)
			continue
		}
		if old.Runtime != j.Runtime || old.Schedule != j.Schedule || old.Command != j.Command || old.Timeout != j.Timeout || old.Enabled != j.Enabled {
			from, to := diffSections(have.Cron[slices.IndexFunc(have.Cron, func(c manifest.CronJob) bool { return c.Name == j.Name })], wantMf.Cron[i])
			add(ManifestChange{Section: "cron", Item: j.Name, Action: "change", From: from, To: to})
			ops.updateCron[old.ID] = req
		}
	}
	for _, name := range sortedKeys(haveJobs) {
		if removal(ManifestChange{Section: "cron", Item: name, Action: "remove"}) {
			ops.removeCron = append(ops.removeCron, haveJobs[name])
		}
	}

	if plan.Changes == nil {
		plan.Changes = []ManifestChange{}
	}
	if plan.MissingSecrets == nil {
		plan.MissingSecrets = []string{}
	}
	plan.InSync = len(plan.Changes) == 0
	return plan, ops, nil
}

// planEnv merges the project's variables with the manifest's. It reports changes through
// add/removal and returns the new list and whether it differs.
func (m *Manager) planEnv(cur store.Project, mf manifest.Manifest, opts ManifestOptions, add func(ManifestChange), removal func(ManifestChange) bool, plan *ManifestPlan) ([]EnvVarRequest, bool) {
	var out []EnvVarRequest
	changed := false
	seen := map[string]bool{}
	for _, e := range cur.Env {
		seen[e.Key] = true
		if v, ok := mf.Env[e.Key]; ok {
			if e.IsSecret || e.Value != v {
				c := ManifestChange{Section: "env", Item: e.Key, Action: "change", To: v}
				if !e.IsSecret {
					c.From = e.Value
				}
				add(c)
				changed = true
			}
			out = append(out, EnvVarRequest{Key: e.Key, Value: v})
			continue
		}
		if slices.Contains(mf.Secrets, e.Key) {
			value := e.Value
			if v, ok := opts.Secrets[e.Key]; ok && v != e.Value {
				value = v
				add(ManifestChange{Section: "env", Item: e.Key, Action: "change"})
				changed = true
			} else if !e.IsSecret {
				add(ManifestChange{Section: "env", Item: e.Key, Action: "change", From: "plain", To: "secret"})
				changed = true
			}
			if value == "" {
				plan.MissingSecrets = append(plan.MissingSecrets, e.Key)
			}
			out = append(out, EnvVarRequest{Key: e.Key, Value: value, IsSecret: true})
			continue
		}
		if removal(ManifestChange{Section: "env", Item: e.Key, Action: "remove"}) {
			changed = true
			continue
		}
		out = append(out, EnvVarRequest{Key: e.Key, Value: e.Value, IsSecret: e.IsSecret})
	}
	for _, k := range sortedKeys(mf.Env) {
		if seen[k] {
			continue
		}
		add(ManifestChange{Section: "env", Item: k, Action: "add", To: mf.Env[k]})
		out = append(out, EnvVarRequest{Key: k, Value: mf.Env[k]})
		changed = true
	}
	for _, k := range mf.Secrets {
		if seen[k] {
			continue
		}
		value := opts.Secrets[k]
		if value == "" {
			plan.MissingSecrets = append(plan.MissingSecrets, k)
		}
		add(ManifestChange{Section: "env", Item: k, Action: "add"})
		out = append(out, EnvVarRequest{Key: k, Value: value, IsSecret: true})
		changed = true
	}
	sort.Strings(plan.MissingSecrets)
	return out, changed
}

// sectionChange compares one section of two manifests.
func sectionChange[T any](name string, have, want *T) (ManifestChange, bool) {
	switch {
	case have == nil && want == nil:
		return ManifestChange{}, false
	case have == nil:
		return ManifestChange{Section: name, Action: "add", To: describe(*want)}, true
	case want == nil:
		return ManifestChange{Section: name, Action: "remove", From: describe(*have)}, true
	case reflect.DeepEqual(*have, *want):
		return ManifestChange{}, false
	}
	from, to := diffSections(*have, *want)
	return ManifestChange{Section: name, Action: "change", From: from, To: to}, true
}

// sectionFields renders a manifest section as its YAML keys and values.
func sectionFields(v any) map[string]string {
	out := map[string]string{}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return out
	}
	var fields map[string]any
	if yaml.Unmarshal(raw, &fields) != nil {
		return out // "redis: true" – a service without settings
	}
	for k, val := range fields {
		switch x := val.(type) {
		case []any:
			parts := make([]string, len(x))
			for i, p := range x {
				parts[i] = fmt.Sprint(p)
			}
			out[k] = "[" + strings.Join(parts, ", ") + "]"
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// describe summarises a section: "type: mariadb, version: 11.4".
func describe(v any) string {
	f := sectionFields(v)
	parts := make([]string, 0, len(f))
	for _, k := range sortedKeys(f) {
		parts = append(parts, k+": "+f[k])
	}
	return strings.Join(parts, ", ")
}

// diffSections lists the keys whose values differ, as they are and as they will be.
func diffSections(have, want any) (from, to string) {
	a, b := sectionFields(have), sectionFields(want)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var f, t []string
	for _, k := range sortedKeys(keys) {
		if a[k] == b[k] {
			continue
		}
		if v, ok := a[k]; ok {
			f = append(f, k+": "+v)
		} else {
			f = append(f, k+": –")
		}
		if v, ok := b[k]; ok {
			t = append(t, k+": "+v)
		} else {
			t = append(t, k+": –")
		}
	}
	return strings.Join(f, ", "), strings.Join(t, ", ")
}

func workerManifest(w store.Worker) manifest.Worker {
	mw := manifest.Worker{Name: w.Name, Preset: w.Preset}
	if len(w.Args) > 0 {
		mw.Arg = w.Args[0]
	}
	if !w.Enabled {
		off := false
		mw.Enabled = &off
	}
	return mw
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- apply ------------------------------------------------------------------

// ApplyManifest brings a project in line with a manifest: services through Update (one
// restart), then workers, cron jobs and domains. Removals happen only with Prune.
func (m *Manager) ApplyManifest(ctx context.Context, id string, mf manifest.Manifest, opts ManifestOptions) (ManifestResult, error) {
	plan, ops, err := m.planManifest(ctx, id, mf, opts)
	if err != nil {
		return ManifestResult{}, err
	}
	// Workers and cron jobs that go away first: removing a runtime keeps their
	// definitions, and a worker update may need the runtime the update adds.
	for _, w := range ops.removeWorkers {
		if err := m.RemoveWorker(ctx, id, w.ID); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("remove worker %s: %w", w.Name, err)
		}
	}
	for _, j := range ops.removeCron {
		if err := m.RemoveCronJob(ctx, id, j.ID); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("remove cron job %s: %w", j.Name, err)
		}
	}
	if ops.hasUpdate() {
		if _, err := m.Update(ctx, id, ops.update); err != nil {
			return ManifestResult{Plan: plan}, err
		}
	}
	if ops.databaseAdd != nil || len(ops.databasesAdd) > 0 {
		if _, err := m.Update(ctx, id, UpdateRequest{Database: ops.databaseAdd, Databases: ops.databasesAdd}); err != nil {
			return ManifestResult{Plan: plan}, err
		}
	}
	if ops.limits != nil {
		if _, err := m.SetLimits(ctx, id, *ops.limits); err != nil {
			return ManifestResult{Plan: plan}, err
		}
	}
	if ops.health != nil {
		if _, err := m.SetHealthCheck(ctx, id, *ops.health); err != nil {
			return ManifestResult{Plan: plan}, err
		}
	}
	for wid, req := range ops.updateWorkers {
		if _, err := m.UpdateWorker(ctx, id, wid, req); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("worker %s: %w", req.Name, err)
		}
	}
	for _, req := range ops.addWorkers {
		if _, err := m.AddWorker(ctx, id, req); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("worker %s: %w", req.Name, err)
		}
	}
	for jid, req := range ops.updateCron {
		if _, err := m.UpdateCronJob(ctx, id, jid, req); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("cron job %s: %w", req.Name, err)
		}
	}
	for _, req := range ops.addCron {
		if _, err := m.AddCronJob(ctx, id, req); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("cron job %s: %w", req.Name, err)
		}
	}
	for _, d := range ops.removeDomains {
		if err := m.RemoveDomain(ctx, id, d.ID); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("remove domain %s: %w", d.Hostname, err)
		}
	}
	for _, h := range ops.addDomains {
		if _, err := m.AddDomain(ctx, id, h); err != nil {
			return ManifestResult{Plan: plan}, fmt.Errorf("domain %s: %w", h, err)
		}
	}
	var view View
	if opts.Start {
		view, err = m.Start(ctx, id)
	} else {
		view, err = m.Get(ctx, id)
	}
	if err != nil {
		return ManifestResult{Plan: plan}, err
	}
	return ManifestResult{Plan: plan, View: view}, nil
}

// CreateFromManifest creates a project from a manifest: services, variables and the
// repository in one Create, then workers, cron jobs and domains. The project starts
// only once all of them are in place.
func (m *Manager) CreateFromManifest(ctx context.Context, mf manifest.Manifest, req ManifestCreateRequest) (ManifestResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = mf.Name
	}
	if name == "" {
		return ManifestResult{}, fmt.Errorf("%w: the project needs a name – set name in %s or pass one", validate.ErrInvalid, manifest.FileName)
	}
	for k := range req.Secrets {
		if !slices.Contains(mf.Secrets, k) {
			return ManifestResult{}, fmt.Errorf("%w: %s is not declared under secrets in %s", validate.ErrInvalid, k, manifest.FileName)
		}
	}
	if hasExternal(mf) {
		return ManifestResult{}, fmt.Errorf("%w: %s connects to an external server, and its password is never in the file; create the project without the manifest and add the connection in Envoryx", validate.ErrInvalid, manifest.FileName)
	}
	// Workers and cron jobs are checked before anything is created.
	if _, _, err := m.desiredState(mf, name); err != nil {
		return ManifestResult{}, err
	}
	cr := manifestRequest(mf, name)
	cr.Path = req.Path
	cr.Git = req.Git
	for _, k := range sortedKeys(mf.Env) {
		cr.Env = append(cr.Env, EnvVarRequest{Key: k, Value: mf.Env[k]})
	}
	for _, k := range mf.Secrets {
		cr.Env = append(cr.Env, EnvVarRequest{Key: k, Value: req.Secrets[k], IsSecret: true})
	}
	cr.CreateStarter = req.Git == nil || req.Git.URL == ""
	view, err := m.Create(ctx, cr)
	if err != nil {
		return ManifestResult{}, err
	}
	res, err := m.ApplyManifest(ctx, view.Project.ID, mf, ManifestOptions{Secrets: req.Secrets, Start: req.Start})
	if err != nil {
		return ManifestResult{View: view}, fmt.Errorf("project %s was created, but %w; run \"envoryx up\" again to finish", view.Project.Slug, err)
	}
	return res, nil
}

// CreateFromRepository creates a project with a repository and, when the clone brings
// an envoryx.yml, applies it before the project starts; the manifest then wins over the
// request's services (the fresh project has no data to lose, so removals happen). The
// plan is nil when the repository has no manifest.
func (m *Manager) CreateFromRepository(ctx context.Context, req CreateRequest) (View, *ManifestPlan, error) {
	start := req.Start
	req.Start = false
	view, err := m.Create(ctx, req)
	if err != nil {
		return View{}, nil, err
	}
	mf, found, err := m.ReadRepositoryManifest(ctx, view.Project.ID)
	if err != nil {
		return view, nil, fmt.Errorf("project %s was created, but its %s is not usable: %w", view.Project.Slug, manifest.FileName, err)
	}
	if !found {
		if start {
			view, err = m.Start(ctx, view.Project.ID)
		}
		return view, nil, err
	}
	res, err := m.ApplyManifest(ctx, view.Project.ID, mf, ManifestOptions{Prune: true, Start: start})
	if err != nil {
		return view, &res.Plan, fmt.Errorf("project %s was created, but applying %s failed: %w", view.Project.Slug, manifest.FileName, err)
	}
	return res.View, &res.Plan, nil
}

// ---- the file in the project directory --------------------------------------

// projectRoot opens the project directory as a root no path can escape (symlinks in
// the repository included).
func (m *Manager) projectRoot(ctx context.Context, id string) (*os.Root, store.Project, error) {
	if err := validate.UUID(id); err != nil {
		return nil, store.Project{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return nil, store.Project{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return nil, store.Project{}, err
	}
	dir, err := validate.ResolveUnder(planner.paths.ProjectsDir, p.Path)
	if err != nil {
		return nil, store.Project{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, store.Project{}, fmt.Errorf("open project directory: %w", err)
	}
	return root, p, nil
}

// ReadRepositoryManifest reads envoryx.yml from the project directory; found is false
// when there is none.
func (m *Manager) ReadRepositoryManifest(ctx context.Context, id string) (mf manifest.Manifest, found bool, err error) {
	root, _, err := m.projectRoot(ctx, id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest.Manifest{}, false, nil
		}
		return manifest.Manifest{}, false, err
	}
	defer root.Close()
	f, err := root.Open(manifest.FileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest.Manifest{}, false, nil
		}
		return manifest.Manifest{}, false, err
	}
	defer f.Close()
	// One byte past the limit is enough for Parse to refuse an oversized file.
	data, err := io.ReadAll(io.LimitReader(f, manifest.MaxSize+1))
	if err != nil {
		return manifest.Manifest{}, true, err
	}
	mf, err = manifest.Parse(data)
	return mf, true, err
}

// RepositoryManifestStatus reads the project's envoryx.yml and compares it with the
// project (with prune, so removals show up as skipped).
func (m *Manager) RepositoryManifestStatus(ctx context.Context, id string) (RepositoryManifest, error) {
	mf, found, err := m.ReadRepositoryManifest(ctx, id)
	if !found {
		return RepositoryManifest{}, err
	}
	if err != nil {
		if errors.Is(err, validate.ErrInvalid) {
			return RepositoryManifest{Present: true, Error: err.Error()}, nil
		}
		return RepositoryManifest{}, err
	}
	plan, err := m.PlanManifest(ctx, id, mf, ManifestOptions{})
	if err != nil {
		if errors.Is(err, validate.ErrInvalid) {
			return RepositoryManifest{Present: true, Error: err.Error()}, nil
		}
		return RepositoryManifest{}, err
	}
	return RepositoryManifest{Present: true, Plan: &plan}, nil
}

// WriteRepositoryManifest writes the project's manifest into its directory, owned by
// the project user, replacing an existing envoryx.yml.
func (m *Manager) WriteRepositoryManifest(ctx context.Context, id string) ([]byte, error) {
	mf, err := m.ExportManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	data, err := manifest.Marshal(mf)
	if err != nil {
		return nil, err
	}
	root, _, err := m.projectRoot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.WriteFile(manifest.FileName, data, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", manifest.FileName, err)
	}
	if planner, err := m.planner(); err == nil && os.Geteuid() == 0 {
		_ = root.Lchown(manifest.FileName, planner.paths.PUID, planner.paths.PGID)
	}
	return data, nil
}

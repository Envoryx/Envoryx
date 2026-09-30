package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// extraOwnsVolume reports whether an auxiliary service keeps persistent data.
func extraOwnsVolume(kind store.ServiceKind) bool {
	switch kind {
	case store.ServiceRedis, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceStorage:
		return true
	}
	return false
}

// extraAlwaysPublished reports whether an auxiliary service's primary port is published
// whatever the request says: it carries a web UI (Mailpit's inbox, Meilisearch's
// dashboard).
func extraAlwaysPublished(kind store.ServiceKind) bool {
	return kind == store.ServiceMailpit || kind == store.ServiceMeilisearch || kind == store.ServiceOpenSearchDashboards
}

// extraKinds are the auxiliary services ExtraServices describes. OpenSearch Dashboards is
// described as part of OpenSearch.
var extraKinds = []store.ServiceKind{store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOllama}

// extraPortKinds are the auxiliary services with a ServiceConfig whose ports count as used.
var extraPortKinds = append(slices.Clone(extraKinds), store.ServiceOpenSearchDashboards)

// setWebUIPort stores the host port of RabbitMQ's management UI.
func setWebUIPort(svc *store.ProjectService, port int) error {
	var cfg runtime.ServiceConfig
	if len(svc.Config) > 0 {
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
	}
	cfg.WebUIPort = port
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	svc.Config = raw
	return nil
}

// ExtraServices describes the auxiliary services of a project for the UI.
func (m *Manager) ExtraServices(ctx context.Context, id string) ([]ExtraServiceInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []ExtraServiceInfo
	for _, svc := range view.Project.Services {
		if !svc.Enabled || !slices.Contains(extraKinds, svc.Kind) {
			continue
		}
		var cfg runtime.ServiceConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		info := ExtraServiceInfo{Kind: svc.Kind, Version: svc.Version, Image: svc.Image, HostPort: cfg.HostPort, State: "missing"}
		switch svc.Kind {
		case store.ServiceRedis:
			info.Host, info.Port, info.VolumeName = "redis", 6379, VolumeName(view.Project.Slug, store.ServiceRedis)
			if cfg.External() {
				// No container: nothing runs, nothing is stored here.
				info.Host, info.Port, info.VolumeName, info.External = cfg.Host, cfg.Port, "", true
			}
			for k := range runtime.RedisEnv(cfg) {
				info.InjectedEnv = append(info.InjectedEnv, k)
			}
		case store.ServiceMemcached:
			info.Host, info.Port = "memcached", runtime.MemcachedPort
			info.InjectedEnv = append(info.InjectedEnv, runtime.MemcachedEnvKeys...)
		case store.ServiceMailpit:
			info.Host, info.Port, info.WebUIPort = "mailpit", 1025, cfg.HostPort
			for k := range runtime.MailpitEnv() {
				info.InjectedEnv = append(info.InjectedEnv, k)
			}
		case store.ServiceRabbitMQ:
			info.Host, info.Port, info.WebUIPort = "rabbitmq", runtime.RabbitMQPort, cfg.WebUIPort
			info.VolumeName = VolumeName(view.Project.Slug, store.ServiceRabbitMQ)
			info.Username = cfg.Username
			info.InjectedEnv = append(info.InjectedEnv, runtime.RabbitMQEnvKeys...)
		case store.ServiceMeilisearch:
			// The dashboard shares the API port.
			info.Host, info.Port, info.WebUIPort = "meilisearch", runtime.MeilisearchPort, cfg.HostPort
			info.VolumeName = VolumeName(view.Project.Slug, store.ServiceMeilisearch)
			info.InjectedEnv = append(info.InjectedEnv, runtime.MeilisearchEnvKeys...)
		case store.ServiceTypesense:
			info.Host, info.Port = "typesense", runtime.TypesensePort
			info.VolumeName = VolumeName(view.Project.Slug, store.ServiceTypesense)
			info.InjectedEnv = append(info.InjectedEnv, runtime.TypesenseEnvKeys...)
		case store.ServiceOllama:
			// No volume of its own: the models live in the store all projects share.
			info.Host, info.Port, info.GPU = "ollama", runtime.OllamaPort, cfg.GPU
			info.InjectedEnv = append(info.InjectedEnv, runtime.OllamaEnvKeys...)
		case store.ServiceOpenSearch:
			info.Host, info.Port = "opensearch", runtime.OpenSearchPort
			info.VolumeName = VolumeName(view.Project.Slug, store.ServiceOpenSearch)
			info.InjectedEnv = append(info.InjectedEnv, runtime.OpenSearchEnvKeys...)
			if dash := view.Project.Service(store.ServiceOpenSearchDashboards); dash != nil && dash.Enabled {
				var dcfg runtime.ServiceConfig
				_ = json.Unmarshal(dash.Config, &dcfg)
				info.WebUIPort = dcfg.HostPort
				info.Dashboards = &DashboardsInfo{Image: dash.Image, State: "missing"}
				for _, s := range view.Status.Services {
					if s.Kind == store.ServiceOpenSearchDashboards {
						info.Dashboards.State, info.Dashboards.Health = s.State, s.Health
					}
				}
			}
		}
		sort.Strings(info.InjectedEnv)
		for _, s := range view.Status.Services {
			if s.Kind == svc.Kind {
				info.State, info.Health = s.State, s.Health
			}
		}
		out = append(out, info)
	}
	if out == nil {
		out = []ExtraServiceInfo{}
	}
	return out, nil
}

// RabbitMQCredentials returns the broker login of a project (operate scope in the API,
// like database credentials).
func (m *Manager) RabbitMQCredentials(ctx context.Context, id string) (RabbitMQCredentials, error) {
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return RabbitMQCredentials{}, err
	}
	svc := p.Service(store.ServiceRabbitMQ)
	if svc == nil || !svc.Enabled {
		return RabbitMQCredentials{}, fmt.Errorf("%w: the project has no RabbitMQ", ErrNotFound)
	}
	var cfg runtime.ServiceConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return RabbitMQCredentials{}, err
	}
	return RabbitMQCredentials{Username: cfg.Username, Password: cfg.Password, URL: runtime.RabbitMQEnv(cfg)["RABBITMQ_URL"]}, nil
}

// SearchCredentials returns the admin key of a project's Meilisearch or Typesense
// (operate scope in the API, like database credentials).
func (m *Manager) SearchCredentials(ctx context.Context, id string, kind store.ServiceKind) (SearchCredentials, error) {
	if kind != store.ServiceMeilisearch && kind != store.ServiceTypesense {
		return SearchCredentials{}, fmt.Errorf("%w: %s is not a search engine", validate.ErrInvalid, kind)
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return SearchCredentials{}, err
	}
	svc := p.Service(kind)
	if svc == nil || !svc.Enabled {
		return SearchCredentials{}, fmt.Errorf("%w: the project has no %s", ErrNotFound, kind)
	}
	var cfg runtime.ServiceConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return SearchCredentials{}, err
	}
	if kind == store.ServiceMeilisearch {
		return SearchCredentials{APIKey: cfg.APIKey, URL: runtime.MeilisearchEnv(cfg)["MEILISEARCH_URL"]}, nil
	}
	return SearchCredentials{APIKey: cfg.APIKey, URL: runtime.TypesenseEnv(cfg)["TYPESENSE_URL"]}, nil
}

// syncOpenSearchDashboards keeps OpenSearch Dashboards in step with OpenSearch: on or off
// as want says (nil leaves it), never without OpenSearch, and always on OpenSearch's
// version, since Dashboards refuses to talk to another one. Callers hold the project lock.
func (m *Manager) syncOpenSearchDashboards(ctx context.Context, id string, want *bool, changes map[string]any) error {
	const kind = store.ServiceOpenSearchDashboards
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	search, dash := p.Service(store.ServiceOpenSearch), p.Service(kind)
	enabled := dash != nil
	if want != nil {
		enabled = *want
	}
	if search == nil || !search.Enabled {
		if want != nil && *want {
			return fmt.Errorf("%w: OpenSearch Dashboards needs OpenSearch", validate.ErrInvalid)
		}
		enabled = false
	}
	switch {
	case !enabled && dash == nil:
		return nil

	case !enabled:
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return err
		}
		for _, c := range containers {
			if c.Service() == string(kind) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return fmt.Errorf("remove %s container: %w", kind, err)
				}
			}
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return err
		}
		changes[string(kind)] = "removed"
		return nil

	case dash == nil:
		svc, err := m.buildExtraService(kind, search.Version)
		if err != nil {
			return err
		}
		svc.ProjectID = p.ID
		taken := []int{p.HTTPPort}
		var cfg runtime.ServiceConfig
		if json.Unmarshal(search.Config, &cfg) == nil && cfg.HostPort > 0 {
			taken = append(taken, cfg.HostPort)
		}
		port, err := m.allocatePort(ctx, taken...)
		if err != nil {
			return err
		}
		if err := setHostPort(&svc, port); err != nil {
			return err
		}
		if err := m.store.Projects.AddService(ctx, svc); err != nil {
			return err
		}
		changes[string(kind)] = svc.Version
		return nil

	case dash.Version != search.Version:
		v, err := m.catalog.Resolve(string(kind), search.Version)
		if err != nil {
			return err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, kind, v.Version, v.Image, dash.Config); err != nil {
			return err
		}
		changes[string(kind)] = v.Version
	}
	return nil
}

// applyExtraUpdate adds, changes or removes Redis/Memcached/Mailpit/RabbitMQ/Meilisearch/
// Typesense/OpenSearch/Ollama. Callers hold the project lock.
// It returns whether application containers must be recreated (their env changes).
func (m *Manager) applyExtraUpdate(ctx context.Context, p store.Project, kind store.ServiceKind, upd ExtraUpdate, changes map[string]any) (bool, error) {
	svc := p.Service(kind)
	name := string(kind)
	enabled := svc != nil
	if upd.Enabled != nil {
		enabled = *upd.Enabled
	} else if svc == nil {
		return false, fmt.Errorf("%w: the project has no %s; add it with enabled: true", validate.ErrInvalid, name)
	}
	exposeAsked := upd.ExposePort != nil && *upd.ExposePort
	switch {
	case !enabled && svc == nil:
		return false, nil

	case !enabled && externalService(svc):
		// Envoryx forgets the address; the server is not ours to touch.
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return false, err
		}
		changes[name] = "removed"
		return true, nil

	case !enabled:
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return false, err
		}
		for _, c := range containers {
			if c.Service() == name {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return false, fmt.Errorf("remove %s container: %w", name, err)
				}
			}
		}
		if extraOwnsVolume(kind) {
			if upd.RemoveData {
				if err := m.removeKeptData(ctx, p, kind); err != nil {
					return false, err
				}
			} else {
				// The volume stays; so do the credentials its data was initialised with,
				// which the image applies to an empty volume only.
				if err := m.store.Projects.KeepService(ctx, store.KeptService{ProjectID: p.ID, Kind: kind, Version: svc.Version, Config: svc.Config}); err != nil {
					return false, err
				}
				changes[name+"Data"] = "kept"
			}
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return false, err
		}
		changes[name] = "removed"
		return true, nil

	case svc == nil:
		newSvc, err := m.buildExtraService(kind, upd.Version)
		if err != nil {
			return false, err
		}
		newSvc.ProjectID = p.ID
		if upd.External != nil {
			if kind != store.ServiceRedis {
				return false, fmt.Errorf("%w: only Redis can be an external server", validate.ErrInvalid)
			}
			if exposeAsked {
				return false, errExternalRedisPort
			}
			if err := setExternalRedis(&newSvc, *upd.External, ""); err != nil {
				return false, err
			}
			if err := m.checkExternalRedis(ctx, p, &newSvc); err != nil {
				return false, err
			}
			if err := m.store.Projects.AddService(ctx, newSvc); err != nil {
				return false, err
			}
			changes[name] = "external"
			return true, nil
		}
		taken := []int{p.HTTPPort}
		if exposeAsked || extraAlwaysPublished(kind) {
			port, err := m.allocatePort(ctx, taken...)
			if err != nil {
				return false, err
			}
			taken = append(taken, port)
			if err := setHostPort(&newSvc, port); err != nil {
				return false, err
			}
		}
		if kind == store.ServiceRabbitMQ {
			port, err := m.allocatePort(ctx, taken...)
			if err != nil {
				return false, err
			}
			if err := setWebUIPort(&newSvc, port); err != nil {
				return false, err
			}
		}
		if kind == store.ServiceOllama && upd.GPU != nil && *upd.GPU {
			if err := m.checkGPU(ctx, p.ID, p.Slug, newSvc.Image); err != nil {
				return false, err
			}
			if err := editConfig(&newSvc, func(c *runtime.ServiceConfig) error { c.GPU = true; return nil }); err != nil {
				return false, err
			}
		}
		// Data kept from an earlier removal comes back with the volume name; it answers to
		// the credentials it was initialised with, not to freshly generated ones.
		reused := false
		if extraOwnsVolume(kind) {
			kept, err := m.store.Projects.KeptService(ctx, p.ID, kind)
			switch {
			case err == nil:
				var old runtime.ServiceConfig
				_ = json.Unmarshal(kept.Config, &old)
				if err := editConfig(&newSvc, func(c *runtime.ServiceConfig) error {
					c.Username, c.Password, c.APIKey = old.Username, old.Password, old.APIKey
					return nil
				}); err != nil {
					return false, err
				}
				reused = true
			case !errors.Is(err, store.ErrNotFound):
				return false, err
			}
		}
		if err := m.store.Projects.AddService(ctx, newSvc); err != nil {
			return false, err
		}
		if reused {
			if err := m.store.Projects.ForgetKeptService(ctx, p.ID, kind); err != nil {
				return false, err
			}
			changes[name+"Data"] = "reused"
		}
		changes[name] = newSvc.Version
		return true, nil

	case externalService(svc):
		return m.updateExternalRedis(ctx, p, svc, upd, changes)

	case upd.External != nil:
		return false, fmt.Errorf("%w: %s runs in a container of the project; remove it first to connect an external server instead", validate.ErrInvalid, name)

	default:
		version := upd.Version
		if version == "" {
			version = svc.Version
		}
		v, err := m.catalog.Resolve(name, version)
		if err != nil {
			return false, err
		}
		var cfg runtime.ServiceConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		expose := cfg.HostPort > 0
		if upd.ExposePort != nil {
			expose = *upd.ExposePort
		}
		expose = expose || extraAlwaysPublished(kind)
		portChanged := false
		if expose && cfg.HostPort == 0 {
			port, err := m.allocatePort(ctx, p.HTTPPort)
			if err != nil {
				return false, err
			}
			cfg.HostPort, portChanged = port, true
		} else if !expose && cfg.HostPort > 0 {
			cfg.HostPort, portChanged = 0, true
		}
		if kind == store.ServiceRabbitMQ && cfg.WebUIPort == 0 {
			port, err := m.allocatePort(ctx, p.HTTPPort, cfg.HostPort)
			if err != nil {
				return false, err
			}
			cfg.WebUIPort, portChanged = port, true
		}
		// The GPUs are handed over when the container is created, like a port.
		gpuChanged := false
		if kind == store.ServiceOllama && upd.GPU != nil && *upd.GPU != cfg.GPU {
			if *upd.GPU {
				if err := m.checkGPU(ctx, p.ID, p.Slug, v.Image); err != nil {
					return false, err
				}
			}
			cfg.GPU, gpuChanged = *upd.GPU, true
			changes[name+"GPU"] = cfg.GPU
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return false, err
		}
		if v.Version != svc.Version {
			changes[name] = v.Version
		}
		if portChanged {
			changes[name+"HostPort"] = cfg.HostPort
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, kind, v.Version, v.Image, raw); err != nil {
			return false, err
		}
		if portChanged || gpuChanged {
			containers, err := m.engine.ListContainers(ctx, true, p.ID)
			if err != nil {
				return false, err
			}
			for _, c := range containers {
				if c.Service() == name {
					if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
						return false, fmt.Errorf("recreate %s container: %w", name, err)
					}
				}
			}
		}
		return false, nil
	}
}

// updateExternalRedis changes the address of an external Redis (tested before it is
// stored). The application containers are recreated when it changed.
func (m *Manager) updateExternalRedis(ctx context.Context, p store.Project, svc *store.ProjectService, upd ExtraUpdate, changes map[string]any) (bool, error) {
	if upd.ExposePort != nil && *upd.ExposePort {
		return false, errExternalRedisPort
	}
	if upd.External == nil {
		return false, nil
	}
	var cfg runtime.ServiceConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return false, err
	}
	updated := *svc
	if err := setExternalRedis(&updated, *upd.External, cfg.Password); err != nil {
		return false, err
	}
	if string(updated.Config) == string(svc.Config) {
		return false, nil
	}
	if err := m.checkExternalRedis(ctx, p, &updated); err != nil {
		return false, err
	}
	if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, svc.Kind, svc.Version, svc.Image, updated.Config); err != nil {
		return false, err
	}
	var next runtime.ServiceConfig
	_ = json.Unmarshal(updated.Config, &next)
	changes[string(svc.Kind)+"Connection"] = net.JoinHostPort(next.Host, strconv.Itoa(next.Port))
	return true, nil
}

// KeptData lists the data volumes of removed auxiliary services that were kept and still
// exist. A volume removed by hand (the Docker page) is left out.
func (m *Manager) KeptData(ctx context.Context, id string) ([]KeptDataInfo, error) {
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	kept, err := m.store.Projects.KeptServices(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := []KeptDataInfo{}
	if len(kept) == 0 {
		return out, nil
	}
	volumes, err := m.engine.ListVolumes(ctx, true)
	if err != nil {
		return nil, err
	}
	exists := map[string]bool{}
	for _, v := range volumes {
		exists[v.Name] = true
	}
	for _, k := range kept {
		// A service that is back owns its volume again (the record goes with the add).
		if p.Service(k.Kind) != nil || !exists[VolumeName(p.Slug, k.Kind)] {
			continue
		}
		out = append(out, KeptDataInfo{Kind: k.Kind, Version: k.Version, VolumeName: VolumeName(p.Slug, k.Kind), KeptAt: k.KeptAt})
	}
	return out, nil
}

// DeleteKeptData deletes the kept data volume of a removed auxiliary service.
func (m *Manager) DeleteKeptData(ctx context.Context, id string, kind store.ServiceKind) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if !slices.Contains(extraKinds, kind) || !extraOwnsVolume(kind) {
		return fmt.Errorf("%w: %s keeps no data", validate.ErrInvalid, kind)
	}
	unlock, err := m.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return err
	}
	if p.Service(kind) != nil {
		return fmt.Errorf("%w: %s is part of the project; remove it with its data instead", ErrConflict, kind)
	}
	if _, err := m.store.Projects.KeptService(ctx, p.ID, kind); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: no kept %s data", ErrNotFound, kind)
		}
		return err
	}
	if err := m.removeKeptData(ctx, p, kind); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{string(kind) + "Data": "deleted"}})
	return nil
}

// removeKeptData deletes a service's data volume and the settings kept with it.
func (m *Manager) removeKeptData(ctx context.Context, p store.Project, kind store.ServiceKind) error {
	if err := m.engine.RemoveVolume(ctx, VolumeName(p.Slug, kind)); err != nil {
		return fmt.Errorf("remove %s volume: %w", kind, err)
	}
	return m.store.Projects.ForgetKeptService(ctx, p.ID, kind)
}

// moveKeptData renames the kept data volumes along with the project: Docker cannot rename
// a volume, so each is copied to the new name and the old one removed.
func (m *Manager) moveKeptData(ctx context.Context, proj store.Project, oldSlug string, labels map[string]string) error {
	kept, err := m.store.Projects.KeptServices(ctx, proj.ID)
	if err != nil || len(kept) == 0 {
		return err
	}
	web := proj.Service(store.ServiceWeb)
	if web == nil {
		return errors.New("no web image to copy the volumes with")
	}
	volumes, err := m.engine.ListVolumes(ctx, true)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, v := range volumes {
		exists[v.Name] = true
	}
	for _, k := range kept {
		from, to := VolumeName(oldSlug, k.Kind), VolumeName(proj.Slug, k.Kind)
		if from == to || !exists[from] {
			continue
		}
		step(ctx, "Moving the volume {{from}} to {{to}}", "from", from, "to", to)
		if err := m.copyVolume(ctx, proj, web.Image, from, to, labels); err != nil {
			return err
		}
		if err := m.engine.RemoveVolume(ctx, from); err != nil {
			return fmt.Errorf("remove volume %s: %w", from, err)
		}
	}
	return nil
}

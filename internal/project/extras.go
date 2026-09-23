package project

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

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
var extraKinds = []store.ServiceKind{store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch}

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
			for k := range runtime.RedisEnv() {
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
// version – Dashboards refuses to talk to another one. Callers hold the project lock.
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
// Typesense/OpenSearch. Callers hold the project lock.
// It returns whether application containers must be recreated (their env changes).
func (m *Manager) applyExtraUpdate(ctx context.Context, p store.Project, kind store.ServiceKind, upd ExtraUpdate, changes map[string]any) (bool, error) {
	svc := p.Service(kind)
	name := string(kind)
	switch {
	case !upd.Enabled && svc == nil:
		return false, nil

	case !upd.Enabled:
		if extraOwnsVolume(kind) && !upd.RemoveData {
			return false, fmt.Errorf("%w: removing %s deletes its data volume; confirm with removeData", validate.ErrInvalid, name)
		}
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
			if err := m.engine.RemoveVolume(ctx, VolumeName(p.Slug, kind)); err != nil {
				return false, fmt.Errorf("remove %s volume: %w", name, err)
			}
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return false, err
		}
		changes[name] = "removed"
		return true, nil

	case upd.Enabled && svc == nil:
		newSvc, err := m.buildExtraService(kind, upd.Version)
		if err != nil {
			return false, err
		}
		newSvc.ProjectID = p.ID
		taken := []int{p.HTTPPort}
		if upd.ExposePort || extraAlwaysPublished(kind) {
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
		if err := m.store.Projects.AddService(ctx, newSvc); err != nil {
			return false, err
		}
		changes[name] = newSvc.Version
		return true, nil

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
		expose := upd.ExposePort || extraAlwaysPublished(kind)
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
		if portChanged {
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

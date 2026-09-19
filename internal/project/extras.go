package project

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// extraOwnsVolume reports whether an auxiliary service keeps persistent data.
func extraOwnsVolume(kind store.ServiceKind) bool {
	return kind == store.ServiceRedis || kind == store.ServiceStorage
}

// ExtraServices describes the auxiliary services of a project for the UI.
func (m *Manager) ExtraServices(ctx context.Context, id string) ([]ExtraServiceInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []ExtraServiceInfo
	for _, svc := range view.Project.Services {
		if !svc.Enabled || (svc.Kind != store.ServiceRedis && svc.Kind != store.ServiceMailpit) {
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
		case store.ServiceMailpit:
			info.Host, info.Port, info.WebUIPort = "mailpit", 1025, cfg.HostPort
			for k := range runtime.MailpitEnv() {
				info.InjectedEnv = append(info.InjectedEnv, k)
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

// applyExtraUpdate adds, changes or removes Redis/Mailpit. Callers hold the project lock.
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
		if upd.ExposePort || kind == store.ServiceMailpit {
			port, err := m.allocatePort(ctx, p.HTTPPort)
			if err != nil {
				return false, err
			}
			if err := setHostPort(&newSvc, port); err != nil {
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
		expose := upd.ExposePort || kind == store.ServiceMailpit
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

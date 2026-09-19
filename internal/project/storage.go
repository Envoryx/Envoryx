package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// StorageHostname is the host name under which the embedded proxy serves a project's S3
// API to the browser (presigned URLs, direct uploads, public asset URLs). Kept one label
// deep like the dev-server name so a single wildcard covers it.
func StorageHostname(slug, base string) string { return slug + "-s3." + base }

// storageConfig returns the storage service and its configuration, or ErrNotFound.
func storageConfig(p store.Project) (*store.ProjectService, runtime.StorageConfig, error) {
	svc := p.Service(store.ServiceStorage)
	if svc == nil || !svc.Enabled {
		return nil, runtime.StorageConfig{}, fmt.Errorf("%w: the project has no object storage", ErrNotFound)
	}
	var cfg runtime.StorageConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return nil, runtime.StorageConfig{}, fmt.Errorf("storage config: %w", err)
	}
	return svc, cfg, nil
}

// storagePublicURL is the browser-reachable URL of the bucket: through the proxy when it
// runs, otherwise the published host port. Scheme follows the proxy's TLS setup.
func (p *Planner) storagePublicURL(slug string, cfg runtime.StorageConfig) string {
	if p.paths.BaseDomain != "" {
		host := StorageHostname(slug, p.paths.BaseDomain)
		if p.paths.ProxyHTTPSPort == 443 {
			return "https://" + host + "/" + cfg.Bucket
		}
		if p.paths.ProxyHTTPSPort > 0 {
			return fmt.Sprintf("https://%s:%d/%s", host, p.paths.ProxyHTTPSPort, cfg.Bucket)
		}
		if p.paths.ProxyHTTPPort == 80 {
			return "http://" + host + "/" + cfg.Bucket
		}
		if p.paths.ProxyHTTPPort > 0 {
			return fmt.Sprintf("http://%s:%d/%s", host, p.paths.ProxyHTTPPort, cfg.Bucket)
		}
	}
	if cfg.HostPort > 0 && p.paths.PublicHost != "" {
		return fmt.Sprintf("http://%s:%d/%s", p.paths.PublicHost, cfg.HostPort, cfg.Bucket)
	}
	return ""
}

// assignStoragePorts publishes the S3 API and the console on two free host ports.
func (m *Manager) assignStoragePorts(ctx context.Context, proj *store.Project, taken *[]int) error {
	svc := proj.Service(store.ServiceStorage)
	if svc == nil {
		return nil
	}
	var cfg runtime.StorageConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return err
	}
	for _, target := range []*int{&cfg.HostPort, &cfg.ConsolePort} {
		if *target > 0 {
			continue
		}
		port, err := m.allocatePort(ctx, *taken...)
		if err != nil {
			return err
		}
		*taken = append(*taken, port)
		*target = port
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	svc.Config = raw
	return nil
}

// StorageInfo describes the project's object storage. Credentials are included only
// with secrets=true (the credentials endpoint); the plain info is safe for read scope.
func (m *Manager) StorageInfo(ctx context.Context, id string, secrets bool) (StorageInfo, error) {
	if err := validate.UUID(id); err != nil {
		return StorageInfo{}, ErrNotFound
	}
	view, err := m.Get(ctx, id)
	if err != nil {
		return StorageInfo{}, err
	}
	svc, cfg, err := storageConfig(view.Project)
	if err != nil {
		return StorageInfo{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return StorageInfo{}, err
	}
	info := StorageInfo{
		Version: svc.Version, Image: svc.Image, Endpoint: "http://s3:9000", PublicURL: planner.storagePublicURL(view.Project.Slug, cfg),
		HostPort: cfg.HostPort, ConsolePort: cfg.ConsolePort, ConsolePath: runtime.StorageConsolePath, Region: runtime.StorageRegion,
		Bucket: cfg.Bucket, PublicRead: cfg.PublicRead, VolumeName: VolumeName(view.Project.Slug, store.ServiceStorage),
		Hostname: StorageHostname(view.Project.Slug, planner.paths.BaseDomain), State: "missing",
	}
	if secrets {
		info.AccessKey, info.SecretKey = cfg.AccessKey, cfg.SecretKey
	}
	info.InjectedEnv = append([]string{}, runtime.StorageEnvKeys...)
	sort.Strings(info.InjectedEnv)
	for _, s := range view.Status.Services {
		if s.Kind == store.ServiceStorage {
			info.State, info.Health = s.State, s.Health
		}
	}
	return info, nil
}

// SetStoragePublicRead switches anonymous reads of the bucket on or off. Takes effect
// immediately on a running server; otherwise at the next start.
func (m *Manager) SetStoragePublicRead(ctx context.Context, id string, public bool) (StorageInfo, error) {
	if err := validate.UUID(id); err != nil {
		return StorageInfo{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return StorageInfo{}, err
	}
	defer unlock()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return StorageInfo{}, err
	}
	svc, cfg, err := storageConfig(p)
	if err != nil {
		return StorageInfo{}, err
	}
	if cfg.PublicRead != public {
		cfg.PublicRead = public
		raw, err := json.Marshal(cfg)
		if err != nil {
			return StorageInfo{}, err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, store.ServiceStorage, svc.Version, svc.Image, raw); err != nil {
			return StorageInfo{}, err
		}
		if p.DesiredState == store.DesiredRunning && p.Lifecycle == store.LifecycleReady {
			if err := m.provisionBucket(ctx, p, cfg); err != nil {
				return StorageInfo{}, err
			}
		}
		m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"storagePublicRead": public}})
	}
	return m.StorageInfo(ctx, id, false)
}

// provisionBucket makes sure the project's bucket exists with the configured policy. It
// runs after the containers started and waits for the server to answer.
func (m *Manager) provisionBucket(ctx context.Context, p store.Project, cfg runtime.StorageConfig) error {
	paths, err := m.paths()
	if err != nil {
		return err
	}
	endpoint := m.storageDial(paths.SelfContainerID, p, cfg)
	if endpoint == "" {
		return fmt.Errorf("object storage: no published port to reach the server from the host")
	}
	prov := m.provisioner
	if prov == nil {
		prov = s3.Default{}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := prov.EnsureBucket(ctx, "http://"+endpoint, cfg.AccessKey, cfg.SecretKey, cfg.Bucket, cfg.PublicRead); err != nil {
		return fmt.Errorf("object storage: bucket %s: %w", cfg.Bucket, err)
	}
	return nil
}

// storageDial is the address of a project's S3 API for Envoryx itself: the container by
// name when Envoryx runs in Docker (it is attached to the project network), the
// published host port on bare metal.
func (m *Manager) storageDial(selfID string, p store.Project, cfg runtime.StorageConfig) string {
	if selfID == "" {
		if cfg.HostPort == 0 {
			return ""
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HostPort))
	}
	return net.JoinHostPort(ContainerName(p.Slug, store.ServiceStorage), strconv.Itoa(runtime.StoragePort))
}

// applyStorageUpdate adds, changes or removes the object storage. Callers hold the
// project lock. It returns whether application containers must be recreated.
func (m *Manager) applyStorageUpdate(ctx context.Context, p store.Project, upd StorageUpdate, changes map[string]any) (bool, error) {
	svc := p.Service(store.ServiceStorage)
	switch {
	case !upd.Enabled && svc == nil:
		return false, nil

	case !upd.Enabled:
		if !upd.RemoveData {
			return false, fmt.Errorf("%w: removing the object storage deletes its bucket volume; confirm with removeData", validate.ErrInvalid)
		}
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return false, err
		}
		for _, c := range containers {
			if c.Service() == string(store.ServiceStorage) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return false, fmt.Errorf("remove storage container: %w", err)
				}
			}
		}
		if err := m.engine.RemoveVolume(ctx, VolumeName(p.Slug, store.ServiceStorage)); err != nil {
			return false, fmt.Errorf("remove storage volume: %w", err)
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, store.ServiceStorage); err != nil {
			return false, err
		}
		changes["storage"] = "removed"
		return true, nil

	case upd.Enabled && svc == nil:
		newSvc, err := m.buildStorageService(p.Slug, upd.Version, upd.PublicRead)
		if err != nil {
			return false, err
		}
		newSvc.ProjectID = p.ID
		p.Services = append(p.Services, newSvc)
		taken := []int{p.HTTPPort}
		if err := m.assignStoragePorts(ctx, &p, &taken); err != nil {
			return false, err
		}
		if err := m.store.Projects.AddService(ctx, p.Services[len(p.Services)-1]); err != nil {
			return false, err
		}
		changes["storage"] = newSvc.Version
		return true, nil

	default:
		version := upd.Version
		if version == "" {
			version = svc.Version
		}
		v, err := m.catalog.Resolve("rustfs", version)
		if err != nil {
			return false, err
		}
		var cfg runtime.StorageConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return false, err
		}
		if upd.PublicRead != nil && *upd.PublicRead != cfg.PublicRead {
			cfg.PublicRead = *upd.PublicRead
			changes["storagePublicRead"] = cfg.PublicRead
		}
		if v.Version != svc.Version {
			changes["storage"] = v.Version
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return false, err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, store.ServiceStorage, v.Version, v.Image, raw); err != nil {
			return false, err
		}
		return false, nil
	}
}

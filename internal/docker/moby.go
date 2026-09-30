package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

// MobyEngine implements Engine on top of the official Moby client.
type MobyEngine struct {
	cli *client.Client
	log *slog.Logger
	// instance is stamped on everything the engine creates; resources labelled with
	// another instance count as unmanaged (see Owns).
	instance string

	credsMu sync.Mutex
	creds   RegistryCredentials
}

// Options configure the engine connection.
type Options struct {
	// Host overrides the Docker endpoint; empty uses DOCKER_HOST or the default socket.
	Host string
	// Instance is this Envoryx instance's ID (LabelInstance).
	Instance string
}

// InstanceID implements Engine.
func (e *MobyEngine) InstanceID() string { return e.instance }

// owns reports whether a resource with these labels is this instance's to manage.
func (e *MobyEngine) owns(labels map[string]string) bool { return Owns(e.instance, labels) }

// foreign reports whether another Envoryx instance manages a resource with these labels.
func (e *MobyEngine) foreign(labels map[string]string) bool {
	return IsManaged(labels) && !e.owns(labels)
}

// checkVolumeName fails with ErrOtherInstance when the volume exists and belongs to
// another Envoryx instance; Docker would otherwise hand it out as if it were new.
func (e *MobyEngine) checkVolumeName(ctx context.Context, name string) error {
	res, err := e.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return wrap(err)
	}
	if e.foreign(res.Volume.Labels) {
		return fmt.Errorf("volume %s: %w", name, ErrOtherInstance)
	}
	return nil
}

// Connect creates a Moby-backed engine. It does not fail if the daemon is unreachable; use
// Ping to check connectivity.
func Connect(opts Options, log *slog.Logger) (*MobyEngine, error) {
	clientOpts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if opts.Host != "" {
		clientOpts = append(clientOpts, client.WithHost(opts.Host))
	}
	cli, err := client.New(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &MobyEngine{cli: cli, log: log, instance: opts.Instance}, nil
}

// Close releases the client.
func (e *MobyEngine) Close() error { return e.cli.Close() }

func wrap(err error) error {
	if err == nil {
		return nil
	}
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if client.IsErrConnectionFailed(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}

// Ping implements Engine.
func (e *MobyEngine) Ping(ctx context.Context) (Info, error) {
	ping, err := e.cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		return Info{}, wrap(err)
	}
	info, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, wrap(err)
	}
	return Info{
		APIVersion:    ping.APIVersion,
		ServerVersion: info.Info.ServerVersion,
		Hostname:      info.Info.Name,
		OS:            info.Info.OperatingSystem,
		Architecture:  info.Info.Architecture,
		Containers:    info.Info.Containers,
		Running:       info.Info.ContainersRunning,
		NCPU:          info.Info.NCPU,
		MemTotal:      info.Info.MemTotal,
		KernelVersion: info.Info.KernelVersion,
	}, nil
}

func managedFilter(projectID string) client.Filters {
	f := client.Filters{}
	f.Add("label", LabelManaged+"=true")
	if projectID != "" {
		f.Add("label", LabelProjectID+"="+projectID)
	}
	return f
}

// ListContainers implements Engine.
func (e *MobyEngine) ListContainers(ctx context.Context, managedOnly bool, projectID string) ([]Container, error) {
	opts := client.ContainerListOptions{All: true}
	if managedOnly || projectID != "" {
		opts.Filters = managedFilter(projectID)
	}
	res, err := e.cli.ContainerList(ctx, opts)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		// Docker's label filter cannot say "this instance or none", so another
		// instance's containers are dropped here.
		ct := summaryToContainer(c)
		ct.Managed = e.owns(ct.Labels)
		if (managedOnly || projectID != "") && !ct.Managed {
			continue
		}
		out = append(out, ct)
	}
	return out, nil
}

// summaryToContainer converts a listing entry; the caller sets Managed.
func summaryToContainer(c container.Summary) Container {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	ports := make([]PortMapping, 0, len(c.Ports))
	for _, p := range c.Ports {
		if p.PublicPort == 0 || slices.ContainsFunc(ports, func(m PortMapping) bool {
			// A binding on all interfaces shows up once for 0.0.0.0 and once for ::.
			return m.HostPort == int(p.PublicPort) && m.ContainerPort == int(p.PrivatePort) && m.Protocol == p.Type
		}) {
			continue
		}
		ip := ""
		if p.IP.IsValid() {
			ip = p.IP.String()
		}
		ports = append(ports, PortMapping{HostIP: ip, HostPort: int(p.PublicPort), ContainerPort: int(p.PrivatePort), Protocol: p.Type})
	}
	// Docker reports "none" for containers without a healthcheck; treat that as no health.
	health := ""
	if c.Health != nil && c.Health.Status != "none" {
		health = string(c.Health.Status)
	}
	return Container{
		ID:      c.ID,
		Name:    name,
		Image:   c.Image,
		ImageID: c.ImageID,
		State:   string(c.State),
		Status:  c.Status,
		Created: time.Unix(c.Created, 0).UTC(),
		Labels:  c.Labels,
		Ports:   ports,
		Health:  health,
	}
}

// inspectRaw inspects any container, managed or not. Only guards and read-only lookups
// use it.
func (e *MobyEngine) inspectRaw(ctx context.Context, idOrName string) (container.InspectResponse, error) {
	res, err := e.cli.ContainerInspect(ctx, idOrName, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, wrap(err)
	}
	return res.Container, nil
}

// guardContainer ensures the target is managed by Envoryx and returns its details.
func (e *MobyEngine) guardContainer(ctx context.Context, idOrName string) (container.InspectResponse, error) {
	c, err := e.inspectRaw(ctx, idOrName)
	if err != nil {
		return container.InspectResponse{}, err
	}
	if c.Config == nil || !e.owns(c.Config.Labels) {
		return container.InspectResponse{}, fmt.Errorf("container %s: %w", idOrName, ErrNotManaged)
	}
	return c, nil
}

// InspectContainer implements Engine. Unmanaged containers are reported as not managed
// so the API layer can never expose their details for mutation.
func (e *MobyEngine) InspectContainer(ctx context.Context, idOrName string) (ContainerDetails, error) {
	c, err := e.guardContainer(ctx, idOrName)
	if err != nil {
		return ContainerDetails{}, err
	}
	d := ContainerDetails{
		Container: Container{
			ID:      c.ID,
			Name:    strings.TrimPrefix(c.Name, "/"),
			Labels:  c.Config.Labels,
			Managed: true,
		},
		Env:   c.Config.Env,
		Image: c.Config.Image,
	}
	d.Container.Image = c.Config.Image
	d.Container.ImageID = c.Image
	if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		d.Created = t
	}
	if c.State != nil {
		d.Running = c.State.Running
		d.ExitCode = c.State.ExitCode
		d.State = stateString(c.State)
		d.Status = c.State.Error
		if t, err := time.Parse(time.RFC3339Nano, c.State.StartedAt); err == nil {
			d.StartedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, c.State.FinishedAt); err == nil {
			d.FinishedAt = t
		}
	}
	for _, m := range c.Mounts {
		d.Mounts = append(d.Mounts, MountPoint{Type: string(m.Type), Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	if c.NetworkSettings != nil {
		for name := range c.NetworkSettings.Networks {
			d.Networks = append(d.Networks, name)
		}
	}
	if c.State != nil {
		d.OOMKilled = c.State.OOMKilled
	}
	if c.HostConfig != nil {
		d.Resources = Resources{NanoCPUs: c.HostConfig.NanoCPUs, MemoryBytes: c.HostConfig.Memory}
		if c.HostConfig.PidsLimit != nil && *c.HostConfig.PidsLimit > 0 {
			d.Resources.PidsLimit = *c.HostConfig.PidsLimit
		}
		for port, bindings := range c.HostConfig.PortBindings {
			for _, b := range bindings {
				hp, _ := strconv.Atoi(b.HostPort)
				ip := ""
				if b.HostIP.IsValid() {
					ip = b.HostIP.String()
				}
				d.Ports = append(d.Ports, PortMapping{HostIP: ip, HostPort: hp, ContainerPort: int(port.Num()), Protocol: string(port.Proto())})
			}
		}
	}
	return d, nil
}

// InspectMounts implements Engine.
func (e *MobyEngine) InspectMounts(ctx context.Context, idOrName string) ([]MountPoint, error) {
	c, err := e.inspectRaw(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	out := make([]MountPoint, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		out = append(out, MountPoint{Type: string(m.Type), Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	return out, nil
}

func stateString(s *container.State) string {
	switch {
	case s.Running && s.Paused:
		return "paused"
	case s.Running:
		return "running"
	case s.Restarting:
		return "restarting"
	case s.Dead:
		return "dead"
	case s.StartedAt == "" || strings.HasPrefix(s.StartedAt, "0001"):
		return "created"
	default:
		return "exited"
	}
}

// CreateContainer implements Engine. Every container it creates drops NET_RAW, can't gain
// privileges, keeps at most 30 MB of logs and doesn't restart unless the spec asks for it.
func (e *MobyEngine) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	if !IsManaged(spec.Labels) {
		return "", fmt.Errorf("refusing to create container without managed label: %w", ErrNotManaged)
	}
	spec.Labels = StampInstance(e.instance, spec.Labels)
	// A project of another instance with the same name has volumes of the same names;
	// mounting one would hand this container that project's data.
	for _, m := range spec.Mounts {
		if m.Type != "volume" {
			continue
		}
		if err := e.checkVolumeName(ctx, m.Source); err != nil {
			return "", err
		}
	}
	cfg := &container.Config{
		Image:      spec.Image,
		Labels:     spec.Labels,
		Env:        spec.Env,
		Cmd:        spec.Cmd,
		Entrypoint: spec.Entrypoint,
		WorkingDir: spec.WorkingDir,
		User:       spec.User,
	}
	if spec.OpenStdin {
		cfg.OpenStdin, cfg.StdinOnce, cfg.AttachStdin = true, true, true
	}
	if spec.StopTimeout > 0 {
		t := spec.StopTimeout
		cfg.StopTimeout = &t
	}
	if spec.Healthcheck != nil && len(spec.Healthcheck.Test) > 0 {
		cfg.Healthcheck = &container.HealthConfig{
			Test:        append([]string{"CMD"}, spec.Healthcheck.Test...),
			Interval:    spec.Healthcheck.Interval,
			Timeout:     spec.Healthcheck.Timeout,
			StartPeriod: spec.Healthcheck.StartPeriod,
			Retries:     spec.Healthcheck.Retries,
		}
	}

	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		CapDrop:       []string{"NET_RAW"},
		SecurityOpt:   []string{"no-new-privileges:true"},
		ExtraHosts:    spec.ExtraHosts,
		LogConfig: container.LogConfig{
			Type:   "json-file",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
	}
	if spec.RestartPolicy == "unless-stopped" {
		host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	}
	if r := spec.Resources; r != nil {
		host.Resources = container.Resources{NanoCPUs: r.NanoCPUs, Memory: r.MemoryBytes}
		if r.MemoryBytes > 0 {
			host.Resources.MemorySwap = r.MemoryBytes // no swap on top of the limit
		}
		if r.PidsLimit > 0 {
			pids := r.PidsLimit
			host.Resources.PidsLimit = &pids
		}
	}
	if spec.GPUs {
		host.Resources.DeviceRequests = []container.DeviceRequest{{Count: -1, Capabilities: [][]string{{"gpu"}}}}
	}
	for _, m := range spec.Mounts {
		mt := mount.Mount{Target: m.Target, ReadOnly: m.ReadOnly}
		switch m.Type {
		case "bind":
			mt.Type = mount.TypeBind
			mt.Source = m.Source
			mt.BindOptions = &mount.BindOptions{CreateMountpoint: true}
		case "volume":
			mt.Type = mount.TypeVolume
			mt.Source = m.Source
		default:
			return "", fmt.Errorf("unsupported mount type %q", m.Type)
		}
		host.Mounts = append(host.Mounts, mt)
	}
	if len(spec.Ports) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		host.PortBindings = network.PortMap{}
		for _, p := range spec.Ports {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			port, err := network.ParsePort(fmt.Sprintf("%d/%s", p.ContainerPort, proto))
			if err != nil {
				return "", fmt.Errorf("invalid port spec: %w", err)
			}
			binding := network.PortBinding{HostPort: strconv.Itoa(p.HostPort)}
			if p.HostIP != "" {
				addr, err := netip.ParseAddr(p.HostIP)
				if err != nil {
					return "", fmt.Errorf("invalid host ip %q: %w", p.HostIP, err)
				}
				binding.HostIP = addr
			}
			cfg.ExposedPorts[port] = struct{}{}
			host.PortBindings[port] = append(host.PortBindings[port], binding)
		}
	}

	var netCfg *network.NetworkingConfig
	if spec.Network != "" {
		host.NetworkMode = container.NetworkMode(spec.Network)
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				spec.Network: {Aliases: spec.NetworkAlias},
			},
		}
	}

	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             spec.Name,
		Config:           cfg,
		HostConfig:       host,
		NetworkingConfig: netCfg,
	})
	if err != nil {
		if cerrdefs.IsConflict(err) && spec.Name != "" {
			if c, ierr := e.inspectRaw(ctx, spec.Name); ierr == nil && c.Config != nil && e.foreign(c.Config.Labels) {
				return "", fmt.Errorf("container %s: %w", spec.Name, ErrOtherInstance)
			}
		}
		return "", wrap(err)
	}
	for _, w := range res.Warnings {
		e.log.Warn("docker create warning", "container", spec.Name, "warning", w)
	}
	return res.ID, nil
}

// StartContainer implements Engine.
func (e *MobyEngine) StartContainer(ctx context.Context, id string) error {
	if _, err := e.guardContainer(ctx, id); err != nil {
		return err
	}
	_, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return gpuError(wrap(err))
}

// gpuError names the reason when a start failed because Docker has no way to hand over
// GPUs: no nvidia runtime (older Docker), or no CDI spec for a GPU vendor (newer Docker).
func gpuError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range []string{"could not select device driver", "failed to discover GPU vendor", "no known GPU vendor", "nvidia-container-cli"} {
		if strings.Contains(msg, s) {
			return fmt.Errorf("%w (%s)", ErrNoGPU, msg)
		}
	}
	return err
}

// StopContainer implements Engine.
func (e *MobyEngine) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	if _, err := e.guardContainer(ctx, id); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	_, err := e.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs})
	return wrap(err)
}

// RestartContainer implements Engine.
func (e *MobyEngine) RestartContainer(ctx context.Context, id string, timeout time.Duration) error {
	if _, err := e.guardContainer(ctx, id); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	_, err := e.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &secs})
	return wrap(err)
}

// RemoveContainer implements Engine. A running container is stopped first, as "docker
// stop" would: removing it by force kills it outright, and a database, Redis or RabbitMQ
// killed like that loses what it had not written yet (Redis everything since its last
// snapshot, MongoDB the writes not yet in its journal). Recreating a container after a
// version or port change goes through here.
func (e *MobyEngine) RemoveContainer(ctx context.Context, id string) error {
	c, err := e.guardContainer(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if c.State != nil && c.State.Running {
		// No timeout of our own: the container's stop timeout applies (what "docker stop"
		// does). A failed stop is not fatal; the forced removal below still takes it.
		_, _ = e.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	}
	_, err = e.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// UpdateResources implements Engine.
func (e *MobyEngine) UpdateResources(ctx context.Context, id string, r Resources) error {
	c, err := e.guardContainer(ctx, id)
	if err != nil {
		return err
	}
	// Docker reads 0 as "leave as it is", so a limit cannot be lifted in place.
	if c.HostConfig != nil && ((r.MemoryBytes == 0 && c.HostConfig.Memory != 0) || (r.NanoCPUs == 0 && c.HostConfig.NanoCPUs != 0)) {
		return ErrNeedsRecreate
	}
	res := container.Resources{NanoCPUs: r.NanoCPUs, Memory: r.MemoryBytes}
	if r.MemoryBytes > 0 {
		res.MemorySwap = r.MemoryBytes
	}
	pids := r.PidsLimit
	if pids <= 0 {
		pids = -1 // unlimited
	}
	res.PidsLimit = &pids
	_, err = e.cli.ContainerUpdate(ctx, id, client.ContainerUpdateOptions{Resources: &res})
	return wrap(err)
}

// WatchOOM implements Engine.
func (e *MobyEngine) WatchOOM(ctx context.Context, fn func(OOMEvent)) error {
	f := make(client.Filters).Add("type", "container").Add("event", "oom").Add("label", LabelManaged+"=true")
	res := e.cli.Events(ctx, client.EventsListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-res.Err:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				err = errors.New("event stream closed")
			}
			return wrap(err)
		case m := <-res.Messages:
			// The attributes carry the container's labels.
			if !e.owns(m.Actor.Attributes) {
				continue
			}
			ev := OOMEvent{ContainerID: m.Actor.ID, Name: m.Actor.Attributes["name"], Labels: m.Actor.Attributes, Time: time.Unix(0, m.TimeNano)}
			if m.TimeNano == 0 {
				ev.Time = time.Unix(m.Time, 0)
			}
			fn(ev)
		}
	}
}

// ContainerStats implements Engine with a single (two-sample) reading.
func (e *MobyEngine) ContainerStats(ctx context.Context, id string) (Stats, error) {
	if _, err := e.guardContainer(ctx, id); err != nil {
		return Stats{}, err
	}
	return e.stats(ctx, id)
}

// ListedContainerStats implements Engine: the labels come from the listing, so the guard
// needs no inspect.
func (e *MobyEngine) ListedContainerStats(ctx context.Context, c Container) (Stats, error) {
	if !e.owns(c.Labels) {
		return Stats{}, fmt.Errorf("container %s: %w", c.ID, ErrNotManaged)
	}
	return e.stats(ctx, c.ID)
}

// stats takes the reading without a guard; callers check the managed label first.
func (e *MobyEngine) stats(ctx context.Context, id string) (Stats, error) {
	res, err := e.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: false, IncludePreviousSample: true})
	if err != nil {
		return Stats{}, wrap(err)
	}
	defer res.Body.Close()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return Stats{ContainerID: id, SampledAt: time.Now().UTC()}, nil
		}
		return Stats{}, fmt.Errorf("decode stats: %w", err)
	}
	st := Stats{
		ContainerID: id,
		CPUPercent:  cpuPercent(s),
		MemoryBytes: memoryUsage(s),
		MemoryLimit: int64(s.MemoryStats.Limit),
		SampledAt:   s.Read,
	}
	for _, n := range s.Networks {
		st.NetRxBytes += n.RxBytes
		st.NetTxBytes += n.TxBytes
	}
	// cgroup v2 reports "read"/"write", cgroup v1 "Read"/"Write".
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			st.BlockRead += e.Value
		case "write":
			st.BlockWritten += e.Value
		}
	}
	return st, nil
}

// VolumeSizes implements Engine.
func (e *MobyEngine) VolumeSizes(ctx context.Context) ([]VolumeSize, error) {
	res, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return nil, wrap(err)
	}
	var out []VolumeSize
	for _, v := range res.Volumes.Items {
		if !e.owns(v.Labels) {
			continue
		}
		size := int64(-1)
		if v.UsageData != nil {
			size = v.UsageData.Size
		}
		out = append(out, VolumeSize{Name: v.Name, Labels: v.Labels, Bytes: size})
	}
	return out, nil
}

func cpuPercent(s container.StatsResponse) float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	if sysDelta <= 0 || cpuDelta <= 0 {
		return 0
	}
	return (cpuDelta / sysDelta) * cpus * 100
}

func memoryUsage(s container.StatsResponse) int64 {
	usage := s.MemoryStats.Usage
	// Page cache doesn't count, as in docker stats. cgroup v2 reports it as
	// "inactive_file", cgroup v1 as "cache".
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < usage {
		usage -= v
	} else if v, ok := s.MemoryStats.Stats["cache"]; ok && v < usage {
		usage -= v
	}
	return int64(usage)
}

// Exec implements Engine.
func (e *MobyEngine) Exec(ctx context.Context, id string, cmd []string, env []string) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, errors.New("exec: empty command")
	}
	if _, err := e.guardContainer(ctx, id); err != nil {
		return ExecResult{}, err
	}
	created, err := e.cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return ExecResult{}, wrap(err)
	}
	attach, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, wrap(err)
	}
	defer attach.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&limitedWriter{w: &stdout}, &limitedWriter{w: &stderr}, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return ExecResult{}, fmt.Errorf("exec output: %w", err)
	}
	insp, err := e.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return ExecResult{}, wrap(err)
	}
	return ExecResult{ExitCode: insp.ExitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// RunOneShot implements Engine.
func (e *MobyEngine) RunOneShot(ctx context.Context, spec ContainerSpec) (ExecResult, error) {
	spec.RestartPolicy = "no"
	id, err := e.CreateContainer(ctx, spec)
	if err != nil {
		return ExecResult{}, err
	}
	// Always clean up, even when the request context is cancelled mid-way.
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = e.RemoveContainer(rctx, id)
	}()
	// The wait is registered before the start so the exit can never be missed. It must
	// ask for the next exit: the default "not-running" condition is already satisfied by a
	// created container and would report exit code 0 before the process has even started.
	wait := e.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return ExecResult{}, gpuError(wrap(err))
	}
	var code int64
	select {
	case res := <-wait.Result:
		code = res.StatusCode
	case err := <-wait.Error:
		return ExecResult{}, wrap(err)
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	}
	rc, err := e.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return ExecResult{}, wrap(err)
	}
	defer rc.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&limitedWriter{w: &stdout}, &limitedWriter{w: &stderr}, rc); err != nil && !errors.Is(err, io.EOF) {
		return ExecResult{}, fmt.Errorf("read output: %w", err)
	}
	return ExecResult{ExitCode: int(code), Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// RunOneShotStream implements Engine.
func (e *MobyEngine) RunOneShotStream(ctx context.Context, spec ContainerSpec, opts ExecStreamOptions) (int, error) {
	spec.RestartPolicy = "no"
	spec.OpenStdin = opts.Stdin != nil
	id, err := e.CreateContainer(ctx, spec)
	if err != nil {
		return -1, err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = e.RemoveContainer(rctx, id)
	}()
	// Attached before the start, so not a byte of output is missed; the wait is
	// registered first for the same reason as in RunOneShot.
	attach, err := e.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: opts.Stdin != nil, Stdout: true, Stderr: true})
	if err != nil {
		return -1, wrap(err)
	}
	defer attach.Close()
	wait := e.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return -1, gpuError(wrap(err))
	}
	inErr := make(chan error, 1)
	if opts.Stdin != nil {
		go func() {
			_, err := io.Copy(attach.Conn, opts.Stdin)
			_ = attach.CloseWrite()
			inErr <- err
		}()
	} else {
		inErr <- nil
	}
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if _, err := stdcopy.StdCopy(stdout, stderr, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return -1, fmt.Errorf("output: %w", err)
	}
	var code int64
	select {
	case res := <-wait.Result:
		code = res.StatusCode
	case err := <-wait.Error:
		return -1, wrap(err)
	case <-ctx.Done():
		return -1, ctx.Err()
	}
	// As in ExecStream: a process that exits without reading all of its input is not an
	// error of its own, a copy that broke is.
	select {
	case err := <-inErr:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
			return -1, fmt.Errorf("input: %w", err)
		}
	default:
	}
	return int(code), nil
}

// OpenTerminal implements Engine.
func (e *MobyEngine) OpenTerminal(ctx context.Context, id string, opts TerminalOptions) (Terminal, error) {
	if len(opts.Cmd) == 0 {
		return nil, errors.New("terminal: empty command")
	}
	if _, err := e.guardContainer(ctx, id); err != nil {
		return nil, err
	}
	created, err := e.cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd:          opts.Cmd,
		Env:          opts.Env,
		User:         opts.User,
		WorkingDir:   opts.WorkingDir,
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  client.ConsoleSize{Height: opts.Rows, Width: opts.Cols},
	})
	if err != nil {
		return nil, wrap(err)
	}
	attach, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: client.ConsoleSize{Height: opts.Rows, Width: opts.Cols}})
	if err != nil {
		return nil, wrap(err)
	}
	return &mobyTerminal{cli: e.cli, execID: created.ID, hijack: attach.HijackedResponse}, nil
}

type mobyTerminal struct {
	cli    *client.Client
	execID string
	hijack client.HijackedResponse
}

func (t *mobyTerminal) Output() io.Reader { return t.hijack.Reader }
func (t *mobyTerminal) Input() io.Writer  { return t.hijack.Conn }
func (t *mobyTerminal) Resize(ctx context.Context, cols, rows uint) error {
	_, err := t.cli.ExecResize(ctx, t.execID, client.ExecResizeOptions{Height: rows, Width: cols})
	return wrap(err)
}
func (t *mobyTerminal) ExitCode(ctx context.Context) (int, error) {
	insp, err := t.cli.ExecInspect(ctx, t.execID, client.ExecInspectOptions{})
	if err != nil {
		return -1, wrap(err)
	}
	if insp.Running {
		return -1, nil
	}
	return insp.ExitCode, nil
}
func (t *mobyTerminal) Close() error {
	t.hijack.Close()
	return nil
}

// StreamLogs implements Engine.
func (e *MobyEngine) StreamLogs(ctx context.Context, id string, opts LogOptions, emit func(LogLine)) error {
	c, err := e.guardContainer(ctx, id)
	if err != nil {
		return err
	}
	lo := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: opts.Follow, Timestamps: true, Tail: opts.Tail}
	if lo.Tail == "" {
		lo.Tail = "all"
	}
	if !opts.Since.IsZero() {
		lo.Since = opts.Since.UTC().Format(time.RFC3339Nano)
	}
	if !opts.Until.IsZero() {
		lo.Until = opts.Until.UTC().Format(time.RFC3339Nano)
	}
	rc, err := e.cli.ContainerLogs(ctx, c.ID, lo)
	if err != nil {
		return wrap(err)
	}
	defer rc.Close()
	// Close the stream when ctx ends so a blocked read returns.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = rc.Close()
		case <-done:
		}
	}()

	stdout := &lineWriter{stream: "stdout", emit: emit}
	stderr := &lineWriter{stream: "stderr", emit: emit}
	if c.Config != nil && c.Config.Tty {
		// TTY containers produce a raw stream without multiplexing headers.
		_, err = io.Copy(stdout, rc)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, rc)
	}
	stdout.flush()
	stderr.flush()
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read logs: %w", err)
	}
	return nil
}

// lineWriter splits a byte stream into lines, parses Docker's timestamp prefix and emits
// LogLines. Partial lines are buffered until the next write or flush.
type lineWriter struct {
	stream string
	emit   func(LogLine)
	buf    []byte
}

const maxLogLine = 64 << 10

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if len(w.buf) > maxLogLine {
				w.emitLine(w.buf)
				w.buf = nil
			}
			return len(p), nil
		}
		w.emitLine(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emitLine(w.buf)
		w.buf = nil
	}
}

func (w *lineWriter) emitLine(raw []byte) {
	line := strings.TrimRight(string(raw), "\r")
	ts := time.Time{}
	if i := strings.IndexByte(line, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, line[:i]); err == nil {
			ts = t
			line = line[i+1:]
		}
	}
	w.emit(LogLine{Time: ts, Stream: w.stream, Text: line})
}

// ExecStream implements Engine.
func (e *MobyEngine) ExecStream(ctx context.Context, id string, opts ExecStreamOptions) (int, error) {
	if len(opts.Cmd) == 0 {
		return -1, errors.New("exec: empty command")
	}
	if _, err := e.guardContainer(ctx, id); err != nil {
		return -1, err
	}
	created, err := e.cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd: opts.Cmd, Env: opts.Env, User: opts.User, WorkingDir: opts.WorkingDir,
		AttachStdin: opts.Stdin != nil, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return -1, wrap(err)
	}
	attach, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return -1, wrap(err)
	}
	defer attach.Close()

	inErr := make(chan error, 1)
	if opts.Stdin != nil {
		go func() {
			_, err := io.Copy(attach.Conn, opts.Stdin)
			_ = attach.CloseWrite()
			inErr <- err
		}()
	} else {
		inErr <- nil
	}
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if _, err := stdcopy.StdCopy(stdout, stderr, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return -1, fmt.Errorf("exec output: %w", err)
	}
	// The output stream ends when the process has exited. Do not wait for the stdin
	// copier: SSH clients often keep their side open until they see the exit status, so
	// blocking here would deadlock (e.g. `dd if=file` never reads stdin). A copy that
	// already finished with a real error (broken restore pipe) is still reported.
	select {
	case err := <-inErr:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
			return -1, fmt.Errorf("exec input: %w", err)
		}
	default:
	}
	insp, err := e.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return -1, wrap(err)
	}
	return insp.ExitCode, nil
}

// limitedWriter caps captured exec output so a runaway command cannot exhaust memory.
type limitedWriter struct {
	w *bytes.Buffer
}

const execOutputLimit = 4 << 20

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.w.Len() < execOutputLimit {
		remaining := execOutputLimit - l.w.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		l.w.Write(p)
	}
	return len(p), nil
}

// ListNetworks implements Engine.
func (e *MobyEngine) ListNetworks(ctx context.Context, managedOnly bool) ([]Network, error) {
	opts := client.NetworkListOptions{}
	if managedOnly {
		opts.Filters = managedFilter("")
	}
	res, err := e.cli.NetworkList(ctx, opts)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Network, 0, len(res.Items))
	for _, n := range res.Items {
		var subnets []string
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() {
				subnets = append(subnets, c.Subnet.String())
			}
		}
		owned := e.owns(n.Labels)
		if managedOnly && !owned {
			continue // another instance's
		}
		out = append(out, Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Labels: n.Labels, Managed: owned, Subnets: subnets})
	}
	return out, nil
}

// CreateNetwork implements Engine.
func (e *MobyEngine) CreateNetwork(ctx context.Context, name string, labels map[string]string, subnet string) (string, error) {
	if !IsManaged(labels) {
		return "", fmt.Errorf("refusing to create network without managed label: %w", ErrNotManaged)
	}
	opts := client.NetworkCreateOptions{Driver: "bridge", Labels: StampInstance(e.instance, labels)}
	if subnet != "" {
		prefix, err := netip.ParsePrefix(subnet)
		if err != nil {
			return "", fmt.Errorf("invalid subnet %q: %w", subnet, err)
		}
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{{Subnet: prefix}}}
	}
	res, err := e.cli.NetworkCreate(ctx, name, opts)
	if err != nil {
		switch msg := err.Error(); {
		case strings.Contains(msg, "overlaps"):
			return "", fmt.Errorf("%w: %v", ErrSubnetInUse, err)
		case strings.Contains(msg, "fully subnetted"):
			return "", fmt.Errorf("%w: %v", ErrAddressPoolsExhausted, err)
		}
		if n, ierr := e.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); ierr == nil && e.foreign(n.Network.Labels) {
			return "", fmt.Errorf("network %s: %w", name, ErrOtherInstance)
		}
		return "", wrap(err)
	}
	return res.ID, nil
}

// RemoveNetwork implements Engine.
func (e *MobyEngine) RemoveNetwork(ctx context.Context, idOrName string) error {
	res, err := e.cli.NetworkInspect(ctx, idOrName, client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return wrap(err)
	}
	if !e.owns(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", idOrName, ErrNotManaged)
	}
	_, err = e.cli.NetworkRemove(ctx, res.Network.ID, client.NetworkRemoveOptions{})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// ConnectNetwork implements Engine.
func (e *MobyEngine) ConnectNetwork(ctx context.Context, networkName, containerID string, aliases ...string) error {
	res, err := e.cli.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{})
	if err != nil {
		return wrap(err)
	}
	if !e.owns(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", networkName, ErrNotManaged)
	}
	opts := client.NetworkConnectOptions{Container: containerID}
	if len(aliases) > 0 {
		opts.EndpointConfig = &network.EndpointSettings{Aliases: aliases}
	}
	_, err = e.cli.NetworkConnect(ctx, res.Network.ID, opts)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return wrap(err)
}

// DisconnectNetwork implements Engine.
func (e *MobyEngine) DisconnectNetwork(ctx context.Context, network, containerID string) error {
	res, err := e.cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return wrap(err)
	}
	if !e.owns(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", network, ErrNotManaged)
	}
	_, err = e.cli.NetworkDisconnect(ctx, res.Network.ID, client.NetworkDisconnectOptions{Container: containerID, Force: true})
	if err != nil && (cerrdefs.IsNotFound(err) || strings.Contains(err.Error(), "is not connected")) {
		return nil
	}
	return wrap(err)
}

// NetworkAddresses implements Engine.
func (e *MobyEngine) NetworkAddresses(ctx context.Context, network string) (string, map[string]string, error) {
	res, err := e.cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return "", nil, wrap(err)
	}
	gateway := ""
	for _, c := range res.Network.IPAM.Config {
		if c.Gateway.Is4() {
			gateway = c.Gateway.String()
			break
		}
	}
	ips := make(map[string]string, len(res.Network.Containers))
	for id, ep := range res.Network.Containers {
		if a := ep.IPv4Address.Addr(); a.Is4() {
			ips[id] = a.String()
		}
	}
	return gateway, ips, nil
}

// NetworkEndpoints implements Engine.
func (e *MobyEngine) NetworkEndpoints(ctx context.Context, network string) ([]Endpoint, error) {
	res, err := e.cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, wrap(err)
	}
	out := make([]Endpoint, 0, len(res.Network.Containers))
	for id, ep := range res.Network.Containers {
		out = append(out, Endpoint{ContainerID: id, Name: ep.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ContainerNetworks implements Engine.
func (e *MobyEngine) ContainerNetworks(ctx context.Context, containerID string) ([]string, error) {
	c, err := e.inspectRaw(ctx, containerID)
	if err != nil {
		return nil, err
	}
	var out []string
	if c.NetworkSettings != nil {
		for name := range c.NetworkSettings.Networks {
			out = append(out, name)
		}
	}
	return out, nil
}

// NetworkAliases implements Engine.
func (e *MobyEngine) NetworkAliases(ctx context.Context, network, containerID string) ([]string, error) {
	c, err := e.inspectRaw(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if c.NetworkSettings == nil || c.NetworkSettings.Networks[network] == nil {
		return nil, nil
	}
	return c.NetworkSettings.Networks[network].Aliases, nil
}

// PortBindings implements Engine.
func (e *MobyEngine) PortBindings(ctx context.Context, containerID string) ([]PortMapping, error) {
	c, err := e.inspectRaw(ctx, containerID)
	if err != nil {
		return nil, err
	}
	var out []PortMapping
	if c.HostConfig != nil {
		for port, bindings := range c.HostConfig.PortBindings {
			for _, b := range bindings {
				hp, _ := strconv.Atoi(b.HostPort)
				ip := ""
				if b.HostIP.IsValid() {
					ip = b.HostIP.String()
				}
				out = append(out, PortMapping{HostIP: ip, HostPort: hp, ContainerPort: int(port.Num()), Protocol: string(port.Proto())})
			}
		}
	}
	return out, nil
}

// NetworkAccess implements Engine.
func (e *MobyEngine) NetworkAccess(ctx context.Context, containerID string) (NetworkAccess, error) {
	c, err := e.inspectRaw(ctx, containerID)
	if err != nil {
		return NetworkAccess{}, err
	}
	out := NetworkAccess{}
	if c.HostConfig != nil {
		out.Mode = string(c.HostConfig.NetworkMode)
	}
	if out.Mode == "host" {
		out.Direct = true
		return out, nil
	}
	if c.NetworkSettings == nil {
		return out, nil
	}
	for name, ep := range c.NetworkSettings.Networks {
		res, err := e.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if err != nil {
			continue
		}
		switch res.Network.Driver {
		case "macvlan", "ipvlan":
			out.Direct = true
			if ep != nil && ep.IPAddress.IsValid() {
				out.IPs = append(out.IPs, ep.IPAddress.String())
			}
		}
	}
	sort.Strings(out.IPs)
	return out, nil
}

// ListVolumes implements Engine.
func (e *MobyEngine) ListVolumes(ctx context.Context, managedOnly bool) ([]Volume, error) {
	opts := client.VolumeListOptions{}
	if managedOnly {
		opts.Filters = managedFilter("")
	}
	res, err := e.cli.VolumeList(ctx, opts)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		owned := e.owns(v.Labels)
		if managedOnly && !owned {
			continue // another instance's
		}
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Labels: v.Labels, Managed: owned})
	}
	return out, nil
}

// CreateVolume implements Engine.
func (e *MobyEngine) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	if !IsManaged(labels) {
		return fmt.Errorf("refusing to create volume without managed label: %w", ErrNotManaged)
	}
	// Docker hands back an existing volume of that name as if it had created it.
	if err := e.checkVolumeName(ctx, name); err != nil {
		return err
	}
	_, err := e.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: StampInstance(e.instance, labels)})
	return wrap(err)
}

// RemoveVolume implements Engine.
func (e *MobyEngine) RemoveVolume(ctx context.Context, name string) error {
	res, err := e.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return wrap(err)
	}
	if !e.owns(res.Volume.Labels) {
		return fmt.Errorf("volume %s: %w", name, ErrNotManaged)
	}
	_, err = e.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// ImageExists implements Engine.
func (e *MobyEngine) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := e.cli.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, wrap(err)
}

// ImageID implements Engine.
func (e *MobyEngine) ImageID(ctx context.Context, ref string) (string, error) {
	res, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", wrap(err)
	}
	return res.ID, nil
}

// ListImages implements Engine.
func (e *MobyEngine) ListImages(ctx context.Context) ([]Image, error) {
	res, err := e.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Image, 0, len(res.Items))
	for _, img := range res.Items {
		tags := make([]string, 0, len(img.RepoTags))
		for _, t := range img.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		out = append(out, Image{ID: img.ID, Tags: tags, Size: img.Size, Created: time.Unix(img.Created, 0).UTC()})
	}
	return out, nil
}

// RemoveImage implements Engine. No force: Docker refuses images used by containers.
func (e *MobyEngine) RemoveImage(ctx context.Context, id string) error {
	_, err := e.cli.ImageRemove(ctx, id, client.ImageRemoveOptions{PruneChildren: true})
	return wrap(err)
}

// TagImage implements Engine.
func (e *MobyEngine) TagImage(ctx context.Context, image, ref string) error {
	_, err := e.cli.ImageTag(ctx, client.ImageTagOptions{Source: image, Target: ref})
	return wrap(err)
}

// UntagImage implements Engine. Force on a tag reference only removes that tag: Docker
// keeps the image (as dangling) while a container uses it and deletes it otherwise.
func (e *MobyEngine) UntagImage(ctx context.Context, ref string) error {
	_, err := e.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: true})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// EnsureImage implements Engine.
func (e *MobyEngine) EnsureImage(ctx context.Context, ref string, progress PullProgress) error {
	exists, err := e.ImageExists(ctx, ref)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return e.PullImage(ctx, ref, progress)
}

// PullImage implements Engine.
func (e *MobyEngine) PullImage(ctx context.Context, ref string, progress PullProgress) error {
	if progress != nil {
		progress("contacting the registry")
	}
	resp, err := e.cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: e.pullAuth(ref)})
	if err != nil {
		return wrap(err)
	}
	defer resp.Close()
	sum := pullSummary{layers: map[string]*layerProgress{}}
	last := ""
	lastAt := time.Time{}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if progress == nil {
			continue
		}
		var cur int64
		var total int64
		if msg.Progress != nil {
			cur, total = msg.Progress.Current, msg.Progress.Total
		}
		text := sum.observe(msg.ID, msg.Status, cur, total)
		// Docker reports every few kilobytes; a step text twice a second is plenty.
		throttled := strings.HasPrefix(text, "downloading") || strings.HasPrefix(text, "extracting")
		if text != "" && text != last && (!throttled || time.Since(lastAt) > 500*time.Millisecond) {
			last, lastAt = text, time.Now()
			progress(text)
		}
	}
	return nil
}

// SetRegistryCredentials implements Engine.
func (e *MobyEngine) SetRegistryCredentials(creds RegistryCredentials) {
	e.credsMu.Lock()
	defer e.credsMu.Unlock()
	e.creds = creds
}

func (e *MobyEngine) credentials() []RegistryCredential {
	e.credsMu.Lock()
	creds := e.creds
	e.credsMu.Unlock()
	if creds == nil {
		return nil
	}
	return creds()
}

// dockerHubAuthKey is the server address Docker files Docker Hub logins under.
const dockerHubAuthKey = "https://index.docker.io/v1/"

// RegistryHost returns the registry host of an image reference ("docker.io" for Docker
// Hub, also for short names like "php:8.4").
func RegistryHost(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ""
	}
	return reference.Domain(named)
}

// normalizeHost folds the spellings of Docker Hub into "docker.io".
func normalizeHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "https://"), "http://"), "/")
	switch host {
	case "index.docker.io", "registry-1.docker.io", "index.docker.io/v1", "hub.docker.com":
		return "docker.io"
	}
	return host
}

// pullAuth returns the encoded login for the registry of ref, or "" for anonymous pulls.
func (e *MobyEngine) pullAuth(ref string) string {
	host := RegistryHost(ref)
	for _, c := range e.credentials() {
		if normalizeHost(c.Host) == host {
			buf, err := json.Marshal(registry.AuthConfig{Username: c.Username, Password: c.Password, ServerAddress: c.Host})
			if err != nil {
				return ""
			}
			return base64.URLEncoding.EncodeToString(buf)
		}
	}
	return ""
}

// buildAuths returns every login, keyed the way the builder looks them up.
func (e *MobyEngine) buildAuths() map[string]registry.AuthConfig {
	out := map[string]registry.AuthConfig{}
	for _, c := range e.credentials() {
		key := normalizeHost(c.Host)
		if key == "docker.io" {
			key = dockerHubAuthKey
		}
		out[key] = registry.AuthConfig{Username: c.Username, Password: c.Password, ServerAddress: key}
	}
	return out
}

// InspectImage implements Engine.
func (e *MobyEngine) InspectImage(ctx context.Context, ref string) (ImageInfo, error) {
	res, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageInfo{}, wrap(err)
	}
	info := ImageInfo{ID: res.ID}
	if cfg := res.Config; cfg != nil {
		info.Labels, info.Entrypoint, info.Cmd, info.User = cfg.Labels, cfg.Entrypoint, cfg.Cmd, cfg.User
	}
	return info, nil
}

// BuildImage implements Engine. It uses the classic builder, which needs no BuildKit
// session and takes the registry logins with the request.
func (e *MobyEngine) BuildImage(ctx context.Context, opts BuildOptions) error {
	res, err := e.cli.ImageBuild(ctx, opts.Context, client.ImageBuildOptions{
		Tags:        []string{opts.Tag},
		Dockerfile:  opts.Dockerfile,
		Labels:      opts.Labels,
		PullParent:  opts.Pull,
		NoCache:     opts.NoCache,
		Remove:      true,
		ForceRemove: true,
		AuthConfigs: e.buildAuths(),
		Version:     build.BuilderV1,
	})
	if err != nil {
		return wrap(err)
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	for {
		var msg struct {
			Stream string `json:"stream"`
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			ErrorText string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("build %s: %w", opts.Tag, err)
		}
		if msg.Error != nil || msg.ErrorText != "" {
			text := msg.ErrorText
			if msg.Error != nil && msg.Error.Message != "" {
				text = msg.Error.Message
			}
			if opts.Output != nil {
				opts.Output(text)
			}
			return fmt.Errorf("build %s: %s", opts.Tag, text)
		}
		if opts.Output == nil {
			continue
		}
		out := msg.Stream
		// Pulls of base images report every layer; only the image-level lines are kept.
		if out == "" && msg.Status != "" && (msg.ID == "" || strings.HasPrefix(msg.Status, "Pulling from")) {
			out = msg.Status + "\n"
		}
		for _, line := range strings.SplitAfter(out, "\n") {
			if line = strings.TrimRight(line, "\r\n"); line != "" {
				opts.Output(line)
			}
		}
	}
}

// pullSummary condenses Docker's per-layer pull messages into one line: overall
// download percentage while layers download, then the extraction phase.
type pullSummary struct {
	layers map[string]*layerProgress
}

type layerProgress struct {
	current, total int64
	phase          int // 0 downloading, 1 extracting, 2 done
}

func (s *pullSummary) observe(id, status string, current, total int64) string {
	if id == "" {
		// Image-level lines: "Pulling from …", "Digest: …", "Status: Downloaded newer image …".
		switch {
		case strings.HasPrefix(status, "Status:"):
			return strings.TrimSpace(strings.TrimPrefix(status, "Status:"))
		case strings.HasPrefix(status, "Digest:"):
			return ""
		}
		return status
	}
	l := s.layers[id]
	if l == nil {
		l = &layerProgress{}
		s.layers[id] = l
	}
	switch status {
	case "Pulling fs layer", "Waiting":
		// Docker announces every layer before the first byte arrives, so the totals below
		// stay honest instead of jumping as layers start.
		l.phase = 0
	case "Downloading":
		l.current, l.total, l.phase = current, total, 0
	case "Download complete", "Extracting":
		l.current, l.phase = l.total, 1
	case "Pull complete", "Already exists":
		l.current, l.phase = l.total, 2
	default:
		return ""
	}
	var cur, tot int64
	counts := [3]int{}
	unknown := 0
	for _, x := range s.layers {
		cur += x.current
		tot += x.total
		counts[x.phase]++
		if x.phase == 0 && x.total == 0 {
			unknown++
		}
	}
	switch {
	case counts[0] > 0 && unknown > 0:
		// Some layer sizes are still unknown: a percentage would be misleading.
		return fmt.Sprintf("downloading (%s so far, %d of %d layers done)", formatBytes(cur), counts[2], len(s.layers))
	case counts[0] > 0:
		return fmt.Sprintf("downloading %d%% (%s of %s)", cur*100/tot, formatBytes(cur), formatBytes(tot))
	case counts[1] > 0:
		return fmt.Sprintf("extracting (%d of %d layers done)", counts[2], len(s.layers))
	}
	return ""
}

func formatBytes(n int64) string {
	const mb = 1 << 20
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	}
	return fmt.Sprintf("%d MB", (n+mb/2)/mb)
}

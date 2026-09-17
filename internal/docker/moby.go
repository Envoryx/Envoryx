package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// MobyEngine implements Engine on top of the official Moby client.
type MobyEngine struct {
	cli *client.Client
	log *slog.Logger
}

// Options configure the engine connection.
type Options struct {
	// Host overrides the Docker endpoint; empty uses DOCKER_HOST or the default socket.
	Host string
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
	return &MobyEngine{cli: cli, log: log}, nil
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
		OS:            info.Info.OperatingSystem,
		Architecture:  info.Info.Architecture,
		Containers:    info.Info.Containers,
		Running:       info.Info.ContainersRunning,
		NCPU:          info.Info.NCPU,
		MemTotal:      info.Info.MemTotal,
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
		out = append(out, summaryToContainer(c))
	}
	return out, nil
}

func summaryToContainer(c container.Summary) Container {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	ports := make([]PortMapping, 0, len(c.Ports))
	for _, p := range c.Ports {
		if p.PublicPort == 0 {
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
		Managed: IsManaged(c.Labels),
		Health:  health,
	}
}

// inspectRaw inspects any container (used internally for guards).
func (e *MobyEngine) inspectRaw(ctx context.Context, idOrName string) (container.InspectResponse, error) {
	res, err := e.cli.ContainerInspect(ctx, idOrName, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, wrap(err)
	}
	return res.Container, nil
}

// guardContainer ensures the target is managed by Staqio and returns its details.
func (e *MobyEngine) guardContainer(ctx context.Context, idOrName string) (container.InspectResponse, error) {
	c, err := e.inspectRaw(ctx, idOrName)
	if err != nil {
		return container.InspectResponse{}, err
	}
	if c.Config == nil || !IsManaged(c.Config.Labels) {
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
	if c.HostConfig != nil {
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

// CreateContainer implements Engine. The spec is translated into a hardened Docker config.
func (e *MobyEngine) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	if !IsManaged(spec.Labels) {
		return "", fmt.Errorf("refusing to create container without managed label: %w", ErrNotManaged)
	}
	cfg := &container.Config{
		Image:      spec.Image,
		Labels:     spec.Labels,
		Env:        spec.Env,
		Cmd:        spec.Cmd,
		WorkingDir: spec.WorkingDir,
		User:       spec.User,
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
	return wrap(err)
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

// RemoveContainer implements Engine.
func (e *MobyEngine) RemoveContainer(ctx context.Context, id string) error {
	if _, err := e.guardContainer(ctx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	_, err := e.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// ContainerStats implements Engine with a single (two-sample) reading.
func (e *MobyEngine) ContainerStats(ctx context.Context, id string) (Stats, error) {
	if _, err := e.guardContainer(ctx, id); err != nil {
		return Stats{}, err
	}
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
	return Stats{
		ContainerID: id,
		CPUPercent:  cpuPercent(s),
		MemoryBytes: memoryUsage(s),
		MemoryLimit: int64(s.MemoryStats.Limit),
		SampledAt:   s.Read,
	}, nil
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
	// cgroup v2 reports page cache in "inactive_file"; cgroup v1 in "cache".
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
		out = append(out, Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Labels: n.Labels, Managed: IsManaged(n.Labels)})
	}
	return out, nil
}

// CreateNetwork implements Engine.
func (e *MobyEngine) CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error) {
	if !IsManaged(labels) {
		return "", fmt.Errorf("refusing to create network without managed label: %w", ErrNotManaged)
	}
	res, err := e.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels})
	if err != nil {
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
	if !IsManaged(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", idOrName, ErrNotManaged)
	}
	_, err = e.cli.NetworkRemove(ctx, res.Network.ID, client.NetworkRemoveOptions{})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
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
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Labels: v.Labels, Managed: IsManaged(v.Labels)})
	}
	return out, nil
}

// CreateVolume implements Engine.
func (e *MobyEngine) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	if !IsManaged(labels) {
		return fmt.Errorf("refusing to create volume without managed label: %w", ErrNotManaged)
	}
	_, err := e.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: labels})
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
	if !IsManaged(res.Volume.Labels) {
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
		progress("pulling " + ref)
	}
	resp, err := e.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return wrap(err)
	}
	defer resp.Close()
	last := ""
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if progress != nil && msg.Status != "" && msg.Status != last {
			last = msg.Status
			progress(msg.Status)
		}
	}
	return nil
}

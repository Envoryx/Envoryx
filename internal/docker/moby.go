package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"syscall"
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
		Hostname:      info.Info.Name,
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

// guardContainer ensures the target is managed by Envoryx and returns its details.
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
	wait := e.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{})
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return ExecResult{}, wrap(err)
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

// ConnectNetwork implements Engine.
func (e *MobyEngine) ConnectNetwork(ctx context.Context, network, containerID string) error {
	res, err := e.cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return wrap(err)
	}
	if !IsManaged(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", network, ErrNotManaged)
	}
	_, err = e.cli.NetworkConnect(ctx, res.Network.ID, client.NetworkConnectOptions{Container: containerID})
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
	if !IsManaged(res.Network.Labels) {
		return fmt.Errorf("network %s: %w", network, ErrNotManaged)
	}
	_, err = e.cli.NetworkDisconnect(ctx, res.Network.ID, client.NetworkDisconnectOptions{Container: containerID, Force: true})
	if err != nil && (cerrdefs.IsNotFound(err) || strings.Contains(err.Error(), "is not connected")) {
		return nil
	}
	return wrap(err)
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
	resp, err := e.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
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

// Package dockertest provides an in-memory Engine implementation for tests.
package dockertest

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seramos/staqio/internal/docker"
)

// FakeContainer is the internal state of a simulated container.
type FakeContainer struct {
	ID      string
	ImageID string
	Spec    docker.ContainerSpec
	State   string
	Created time.Time
	// Foreign marks containers that were not created through the fake (simulated
	// third-party containers on the host).
	Foreign bool
}

// Fake is an in-memory Engine with failure injection.
type Fake struct {
	mu sync.Mutex

	seq        int
	containers map[string]*FakeContainer
	networks   map[string]docker.Network
	volumes    map[string]docker.Volume
	images     map[string]string // ref -> image id
	// Remote maps image refs to the id a pull would deliver. Unset refs pull as "<ref>@v1".
	Remote map[string]string

	// Unavailable makes every call fail with docker.ErrUnavailable.
	Unavailable bool
	// FailCreate maps container names to errors returned from CreateContainer.
	FailCreate map[string]error
	// FailStart maps container names to errors returned from StartContainer.
	FailStart map[string]error
	// FailPull maps image refs to errors returned from EnsureImage.
	FailPull map[string]error
	// PullDelay makes EnsureImage honour context cancellation after this delay.
	PullDelay time.Duration

	// ExecHandler simulates commands run inside containers. It receives the container name
	// and the argv; nil means every command succeeds with empty output.
	ExecHandler func(container string, cmd []string, env []string) (docker.ExecResult, error)
	// Execs records every exec call as "name: argv...".
	Execs []string
	// Logs maps container names to their log lines returned by StreamLogs.
	Logs map[string][]docker.LogLine

	// Calls records every mutating operation in order (e.g. "create:name", "start:name").
	Calls []string
}

// New returns an empty fake engine.
func New() *Fake {
	return &Fake{
		containers: map[string]*FakeContainer{},
		networks:   map[string]docker.Network{},
		volumes:    map[string]docker.Volume{},
		images:     map[string]string{},
		Remote:     map[string]string{},
		FailCreate: map[string]error{},
		FailStart:  map[string]error{},
		FailPull:   map[string]error{},
		Logs:       map[string][]docker.LogLine{},
	}
}

var _ docker.Engine = (*Fake)(nil)

func (f *Fake) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%012d%s", prefix, f.seq, strings.Repeat("0", 52))
}

func (f *Fake) record(s string) { f.Calls = append(f.Calls, s) }

// AddForeignContainer simulates a container Staqio did not create.
func (f *Fake) AddForeignContainer(name, image, state string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID("f")
	f.containers[id] = &FakeContainer{
		ID:      id,
		Spec:    docker.ContainerSpec{Name: name, Image: image, Labels: map[string]string{"com.example.app": name}},
		State:   state,
		Created: time.Now().UTC(),
		Foreign: true,
	}
	return id
}

// AddManagedContainer simulates a Staqio container that already exists (e.g. after a restart).
func (f *Fake) AddManagedContainer(spec docker.ContainerSpec, state string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID("c")
	f.containers[id] = &FakeContainer{ID: id, ImageID: f.images[spec.Image], Spec: spec, State: state, Created: time.Now().UTC()}
	return id
}

// AddNetwork simulates an existing network.
func (f *Fake) AddNetwork(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[name] = docker.Network{ID: f.nextID("n"), Name: name, Driver: "bridge", Labels: labels, Managed: docker.IsManaged(labels)}
}

// AddImage marks an image as present.
func (f *Fake) AddImage(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = ref + "@v1"
}

func (f *Fake) remoteID(ref string) string {
	if id, ok := f.Remote[ref]; ok {
		return id
	}
	return ref + "@v1"
}

// SetState changes a container's state (simulates a crash or external stop).
func (f *Fake) SetState(name, state string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.Spec.Name == name {
			c.State = state
			return true
		}
	}
	return false
}

// Container returns a snapshot of a container by name.
func (f *Fake) Container(name string) (FakeContainer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.Spec.Name == name {
			return *c, true
		}
	}
	return FakeContainer{}, false
}

// ContainerNames returns all container names sorted.
func (f *Fake) ContainerNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.containers {
		out = append(out, c.Spec.Name)
	}
	sort.Strings(out)
	return out
}

// NetworkNames returns all network names sorted.
func (f *Fake) NetworkNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.networks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// VolumeNames returns all volume names sorted.
func (f *Fake) VolumeNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.volumes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (f *Fake) check() error {
	if f.Unavailable {
		return docker.ErrUnavailable
	}
	return nil
}

func (f *Fake) find(idOrName string) (*FakeContainer, bool) {
	if c, ok := f.containers[idOrName]; ok {
		return c, true
	}
	for _, c := range f.containers {
		if c.Spec.Name == idOrName || strings.HasPrefix(c.ID, idOrName) {
			return c, true
		}
	}
	return nil, false
}

func (f *Fake) guard(idOrName string) (*FakeContainer, error) {
	c, ok := f.find(idOrName)
	if !ok {
		return nil, docker.ErrNotFound
	}
	if !docker.IsManaged(c.Spec.Labels) {
		return nil, fmt.Errorf("container %s: %w", idOrName, docker.ErrNotManaged)
	}
	return c, nil
}

func toContainer(c *FakeContainer) docker.Container {
	ports := make([]docker.PortMapping, 0, len(c.Spec.Ports))
	for _, p := range c.Spec.Ports {
		ports = append(ports, docker.PortMapping{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: "tcp"})
	}
	return docker.Container{
		ID:      c.ID,
		Name:    c.Spec.Name,
		Image:   c.Spec.Image,
		ImageID: c.ImageID,
		State:   c.State,
		Status:  c.State,
		Created: c.Created,
		Labels:  c.Spec.Labels,
		Ports:   ports,
		Managed: docker.IsManaged(c.Spec.Labels),
	}
}

// Ping implements docker.Engine.
func (f *Fake) Ping(context.Context) (docker.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return docker.Info{}, err
	}
	running := 0
	for _, c := range f.containers {
		if c.State == "running" {
			running++
		}
	}
	return docker.Info{APIVersion: "1.56", ServerVersion: "fake", OS: "linux", Architecture: "x86_64", Containers: len(f.containers), Running: running, NCPU: 4, MemTotal: 8 << 30}, nil
}

// ListContainers implements docker.Engine.
func (f *Fake) ListContainers(_ context.Context, managedOnly bool, projectID string) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	var out []docker.Container
	for _, c := range f.containers {
		managed := docker.IsManaged(c.Spec.Labels)
		if (managedOnly || projectID != "") && !managed {
			continue
		}
		if projectID != "" && c.Spec.Labels[docker.LabelProjectID] != projectID {
			continue
		}
		out = append(out, toContainer(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InspectContainer implements docker.Engine.
func (f *Fake) InspectContainer(_ context.Context, idOrName string) (docker.ContainerDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return docker.ContainerDetails{}, err
	}
	c, err := f.guard(idOrName)
	if err != nil {
		return docker.ContainerDetails{}, err
	}
	d := docker.ContainerDetails{Container: toContainer(c), Running: c.State == "running", Env: c.Spec.Env, Image: c.Spec.Image}
	for _, m := range c.Spec.Mounts {
		d.Mounts = append(d.Mounts, docker.MountPoint{Type: m.Type, Source: m.Source, Destination: m.Target, ReadOnly: m.ReadOnly})
	}
	if c.Spec.Network != "" {
		d.Networks = []string{c.Spec.Network}
	}
	return d, nil
}

// InspectMounts implements docker.Engine.
func (f *Fake) InspectMounts(_ context.Context, idOrName string) ([]docker.MountPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	c, ok := f.find(idOrName)
	if !ok {
		return nil, docker.ErrNotFound
	}
	var out []docker.MountPoint
	for _, m := range c.Spec.Mounts {
		out = append(out, docker.MountPoint{Type: m.Type, Source: m.Source, Destination: m.Target, ReadOnly: m.ReadOnly})
	}
	return out, nil
}

// CreateContainer implements docker.Engine.
func (f *Fake) CreateContainer(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", err
	}
	if !docker.IsManaged(spec.Labels) {
		return "", docker.ErrNotManaged
	}
	if err := f.FailCreate[spec.Name]; err != nil {
		f.record("create-failed:" + spec.Name)
		return "", err
	}
	for _, c := range f.containers {
		if c.Spec.Name == spec.Name {
			return "", fmt.Errorf("conflict: container name %q already in use", spec.Name)
		}
	}
	if spec.Network != "" {
		if _, ok := f.networks[spec.Network]; !ok {
			return "", fmt.Errorf("network %s: %w", spec.Network, docker.ErrNotFound)
		}
	}
	for _, m := range spec.Mounts {
		if m.Type == "volume" {
			if _, ok := f.volumes[m.Source]; !ok {
				return "", fmt.Errorf("volume %s: %w", m.Source, docker.ErrNotFound)
			}
		}
	}
	imageID, ok := f.images[spec.Image]
	if !ok {
		return "", fmt.Errorf("image %s: %w", spec.Image, docker.ErrNotFound)
	}
	id := f.nextID("c")
	f.containers[id] = &FakeContainer{ID: id, ImageID: imageID, Spec: spec, State: "created", Created: time.Now().UTC()}
	f.record("create:" + spec.Name)
	return id, nil
}

// StartContainer implements docker.Engine.
func (f *Fake) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, err := f.guard(id)
	if err != nil {
		return err
	}
	if err := f.FailStart[c.Spec.Name]; err != nil {
		f.record("start-failed:" + c.Spec.Name)
		return err
	}
	c.State = "running"
	f.record("start:" + c.Spec.Name)
	return nil
}

// StopContainer implements docker.Engine.
func (f *Fake) StopContainer(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, err := f.guard(id)
	if err != nil {
		return err
	}
	c.State = "exited"
	f.record("stop:" + c.Spec.Name)
	return nil
}

// RestartContainer implements docker.Engine.
func (f *Fake) RestartContainer(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, err := f.guard(id)
	if err != nil {
		return err
	}
	c.State = "running"
	f.record("restart:" + c.Spec.Name)
	return nil
}

// RemoveContainer implements docker.Engine.
func (f *Fake) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, ok := f.find(id)
	if !ok {
		return nil
	}
	if !docker.IsManaged(c.Spec.Labels) {
		return fmt.Errorf("container %s: %w", id, docker.ErrNotManaged)
	}
	delete(f.containers, c.ID)
	f.record("remove:" + c.Spec.Name)
	return nil
}

// ContainerStats implements docker.Engine.
func (f *Fake) ContainerStats(_ context.Context, id string) (docker.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return docker.Stats{}, err
	}
	c, err := f.guard(id)
	if err != nil {
		return docker.Stats{}, err
	}
	if c.State != "running" {
		return docker.Stats{ContainerID: c.ID, SampledAt: time.Now().UTC()}, nil
	}
	return docker.Stats{ContainerID: c.ID, CPUPercent: 1.5, MemoryBytes: 32 << 20, MemoryLimit: 8 << 30, SampledAt: time.Now().UTC()}, nil
}

// Exec implements docker.Engine.
func (f *Fake) Exec(_ context.Context, id string, cmd []string, env []string) (docker.ExecResult, error) {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return docker.ExecResult{}, err
	}
	c, err := f.guard(id)
	if err != nil {
		f.mu.Unlock()
		return docker.ExecResult{}, err
	}
	if c.State != "running" {
		f.mu.Unlock()
		return docker.ExecResult{}, fmt.Errorf("container %s is not running", c.Spec.Name)
	}
	name := c.Spec.Name
	f.Execs = append(f.Execs, name+": "+strings.Join(cmd, " "))
	handler := f.ExecHandler
	f.mu.Unlock()
	if handler != nil {
		return handler(name, cmd, env)
	}
	return docker.ExecResult{ExitCode: 0}, nil
}

// StreamLogs implements docker.Engine. With Follow it blocks until ctx is cancelled after
// emitting the configured lines (simulating a live stream with no further output).
func (f *Fake) StreamLogs(ctx context.Context, id string, opts docker.LogOptions, emit func(docker.LogLine)) error {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return err
	}
	c, err := f.guard(id)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	lines := append([]docker.LogLine(nil), f.Logs[c.Spec.Name]...)
	f.mu.Unlock()
	if opts.Tail != "" && opts.Tail != "all" {
		if n, err := strconv.Atoi(opts.Tail); err == nil && n < len(lines) {
			lines = lines[len(lines)-n:]
		}
	}
	for _, l := range lines {
		emit(l)
	}
	if opts.Follow {
		<-ctx.Done()
	}
	return nil
}

// ListNetworks implements docker.Engine.
func (f *Fake) ListNetworks(_ context.Context, managedOnly bool) ([]docker.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	var out []docker.Network
	for _, n := range f.networks {
		if managedOnly && !n.Managed {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateNetwork implements docker.Engine.
func (f *Fake) CreateNetwork(_ context.Context, name string, labels map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", err
	}
	if !docker.IsManaged(labels) {
		return "", docker.ErrNotManaged
	}
	if _, ok := f.networks[name]; ok {
		return "", fmt.Errorf("network %q already exists", name)
	}
	id := f.nextID("n")
	f.networks[name] = docker.Network{ID: id, Name: name, Driver: "bridge", Labels: labels, Managed: true}
	f.record("network-create:" + name)
	return id, nil
}

// RemoveNetwork implements docker.Engine.
func (f *Fake) RemoveNetwork(_ context.Context, idOrName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	for name, n := range f.networks {
		if name == idOrName || n.ID == idOrName {
			if !n.Managed {
				return fmt.Errorf("network %s: %w", name, docker.ErrNotManaged)
			}
			for _, c := range f.containers {
				if c.Spec.Network == name {
					return fmt.Errorf("network %s has active endpoints", name)
				}
			}
			delete(f.networks, name)
			f.record("network-remove:" + name)
			return nil
		}
	}
	return nil
}

// ListVolumes implements docker.Engine.
func (f *Fake) ListVolumes(_ context.Context, managedOnly bool) ([]docker.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	var out []docker.Volume
	for _, v := range f.volumes {
		if managedOnly && !v.Managed {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateVolume implements docker.Engine.
func (f *Fake) CreateVolume(_ context.Context, name string, labels map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	if !docker.IsManaged(labels) {
		return docker.ErrNotManaged
	}
	f.volumes[name] = docker.Volume{Name: name, Driver: "local", Labels: labels, Managed: true}
	f.record("volume-create:" + name)
	return nil
}

// RemoveVolume implements docker.Engine.
func (f *Fake) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	v, ok := f.volumes[name]
	if !ok {
		return nil
	}
	if !v.Managed {
		return fmt.Errorf("volume %s: %w", name, docker.ErrNotManaged)
	}
	delete(f.volumes, name)
	f.record("volume-remove:" + name)
	return nil
}

// ImageExists implements docker.Engine.
func (f *Fake) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return false, err
	}
	_, ok := f.images[ref]
	return ok, nil
}

// ImageID implements docker.Engine.
func (f *Fake) ImageID(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", err
	}
	id, ok := f.images[ref]
	if !ok {
		return "", docker.ErrNotFound
	}
	return id, nil
}

// EnsureImage implements docker.Engine.
func (f *Fake) EnsureImage(ctx context.Context, ref string, progress docker.PullProgress) error {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return err
	}
	if _, ok := f.images[ref]; ok {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	return f.PullImage(ctx, ref, progress)
}

// PullImage implements docker.Engine.
func (f *Fake) PullImage(ctx context.Context, ref string, progress docker.PullProgress) error {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return err
	}
	if err := f.FailPull[ref]; err != nil {
		f.mu.Unlock()
		return err
	}
	delay := f.PullDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	if progress != nil {
		progress("pulling " + ref)
	}
	f.mu.Lock()
	f.images[ref] = f.remoteID(ref)
	f.record("pull:" + ref)
	f.mu.Unlock()
	return nil
}

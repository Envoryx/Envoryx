// Package dockertest provides an in-memory Engine implementation for tests.
package dockertest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
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
	// Extra networks the container was attached to via ConnectNetwork.
	Attached []string
}

// Fake is an in-memory Engine with failure injection.
type Fake struct {
	mu sync.Mutex

	seq        int
	containers map[string]*FakeContainer
	networks   map[string]docker.Network
	volumes    map[string]docker.Volume
	images     map[string]string // ref -> image id
	dangling   map[string]bool   // image ids that lost their tag to a re-pull but still exist
	// Remote maps image refs to the id a pull would deliver. Unset refs pull as "<ref>@v1".
	Remote map[string]string
	// Access is returned by NetworkAccess for any container (zero value = bridge).
	Access docker.NetworkAccess

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

	// OneShotHandler simulates transient containers (RunOneShot). nil = exit 0, no output.
	OneShotHandler func(spec docker.ContainerSpec) (docker.ExecResult, error)
	// OneShots records every RunOneShot spec.
	OneShots []docker.ContainerSpec

	// StreamHandler simulates streamed execs: it receives the container name, argv and the
	// full stdin and returns stdout content plus exit code. nil = exit 0, empty output.
	StreamHandler func(container string, cmd []string, env []string, stdin []byte) (stdout string, code int, err error)

	// ReadsStdin tells the fake whether a streamed command consumes stdin to EOF (the
	// default). Commands that ignore stdin must not block on it – like the real engine.
	ReadsStdin func(cmd []string) bool
	// ExecHandler simulates commands run inside containers. It receives the container name
	// and the argv; nil means every command succeeds with empty output.
	ExecHandler func(container string, cmd []string, env []string) (docker.ExecResult, error)
	// Execs records every exec call as "name: argv...".
	Execs []string
	// Logs maps container names to their log lines returned by StreamLogs.
	Logs         map[string][]docker.LogLine
	terminals    []TerminalRecord
	lastTerminal *FakeTerminal

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
		dangling:   map[string]bool{},
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

// AddForeignContainer simulates a container Envoryx did not create.
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

// AddManagedContainer simulates a Envoryx container that already exists (e.g. after a restart).
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

// SetUnavailable flips Docker availability while operations may be running (the daemon
// dies mid-start), safely with respect to the fake's own locking.
func (f *Fake) SetUnavailable(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Unavailable = down
}

// Dangle removes a tag but keeps its image as dangling – the state an older Envoryx (no
// rollback tags yet) or a re-pull leaves behind.
func (f *Fake) Dangle(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.images[ref]
	if !ok {
		return
	}
	delete(f.images, ref)
	f.dropIfUnreferenced(id, true)
}

// resolveImage returns the id for a tag or a bare image id (Docker accepts both).
func (f *Fake) resolveImage(refOrID string) (string, bool) {
	if id, ok := f.images[refOrID]; ok {
		return id, true
	}
	for _, id := range f.images {
		if id == refOrID {
			return id, true
		}
	}
	if f.dangling[refOrID] {
		return refOrID, true
	}
	return "", false
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

// toContainer builds the listing view. Like Docker's ContainerList (daemon/list.go,
// refreshImage), Image is the reference given at creation unless that reference no longer
// resolves to the container's image id – then it is the id itself.
func (f *Fake) toContainer(c *FakeContainer) docker.Container {
	ports := make([]docker.PortMapping, 0, len(c.Spec.Ports))
	for _, p := range c.Spec.Ports {
		ports = append(ports, docker.PortMapping{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: "tcp"})
	}
	image := c.Spec.Image
	if id, ok := f.resolveImage(image); !ok || id != c.ImageID {
		image = c.ImageID
	}
	return docker.Container{
		ID:      c.ID,
		Name:    c.Spec.Name,
		Image:   image,
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
	return docker.Info{APIVersion: "1.56", ServerVersion: "fake", Hostname: "fakehost", OS: "linux", Architecture: "x86_64", Containers: len(f.containers), Running: running, NCPU: 4, MemTotal: 8 << 30}, nil
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
		out = append(out, f.toContainer(c))
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
	d := docker.ContainerDetails{Container: f.toContainer(c), Running: c.State == "running", Env: c.Spec.Env, Image: c.Spec.Image}
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
	imageID, ok := f.resolveImage(spec.Image)
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

// Terminals records terminal sessions opened through the fake (container name + options).
type TerminalRecord struct {
	Container string
	Opts      docker.TerminalOptions
}

// FakeTerminal echoes input back as output and records resizes.
type FakeTerminal struct {
	pr       *io.PipeReader
	pw       *io.PipeWriter
	mu       sync.Mutex
	resizes  []string
	exitCode int
}

func (t *FakeTerminal) Output() io.Reader { return t.pr }
func (t *FakeTerminal) Input() io.Writer  { return t.pw }
func (t *FakeTerminal) Resize(_ context.Context, cols, rows uint) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resizes = append(t.resizes, fmt.Sprintf("%dx%d", cols, rows))
	return nil
}
func (t *FakeTerminal) Close() error { return t.pw.Close() }

// ExitCode returns the configured exit code (default 0).
func (t *FakeTerminal) ExitCode(context.Context) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, nil
}

// Finish writes final output, sets the exit code and closes the output stream.
func (t *FakeTerminal) Finish(output string, code int) {
	t.mu.Lock()
	t.exitCode = code
	t.mu.Unlock()
	if output != "" {
		_, _ = t.pw.Write([]byte(output))
	}
	_ = t.pw.Close()
}

// Resizes returns the recorded resize calls.
func (t *FakeTerminal) Resizes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.resizes...)
}

// RunOneShot implements docker.Engine.
func (f *Fake) RunOneShot(_ context.Context, spec docker.ContainerSpec) (docker.ExecResult, error) {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return docker.ExecResult{}, err
	}
	if !docker.IsManaged(spec.Labels) {
		f.mu.Unlock()
		return docker.ExecResult{}, docker.ErrNotManaged
	}
	f.OneShots = append(f.OneShots, spec)
	f.record("oneshot:" + spec.Name)
	handler := f.OneShotHandler
	f.mu.Unlock()
	if handler != nil {
		return handler(spec)
	}
	return docker.ExecResult{}, nil
}

// OpenTerminal implements docker.Engine.
func (f *Fake) OpenTerminal(_ context.Context, id string, opts docker.TerminalOptions) (docker.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	c, err := f.guard(id)
	if err != nil {
		return nil, err
	}
	if c.State != "running" {
		return nil, fmt.Errorf("container %s is not running", c.Spec.Name)
	}
	f.terminals = append(f.terminals, TerminalRecord{Container: c.Spec.Name, Opts: opts})
	pr, pw := io.Pipe()
	t := &FakeTerminal{pr: pr, pw: pw}
	f.lastTerminal = t
	return t, nil
}

// Terminals returns the opened terminal sessions.
func (f *Fake) Terminals() []TerminalRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TerminalRecord(nil), f.terminals...)
}

// LastTerminal returns the most recently opened terminal (nil if none).
func (f *Fake) LastTerminal() *FakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTerminal
}

// ExecStream implements docker.Engine.
func (f *Fake) ExecStream(_ context.Context, id string, opts docker.ExecStreamOptions) (int, error) {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return -1, err
	}
	c, err := f.guard(id)
	if err != nil {
		f.mu.Unlock()
		return -1, err
	}
	if c.State != "running" {
		f.mu.Unlock()
		return -1, fmt.Errorf("container %s is not running", c.Spec.Name)
	}
	name := c.Spec.Name
	f.Execs = append(f.Execs, name+": "+strings.Join(opts.Cmd, " "))
	handler, reads := f.StreamHandler, f.ReadsStdin
	f.mu.Unlock()
	var in []byte
	if opts.Stdin != nil && (reads == nil || reads(opts.Cmd)) {
		in, _ = io.ReadAll(opts.Stdin)
	}
	if handler == nil {
		return 0, nil
	}
	out, code, err := handler(name, opts.Cmd, opts.Env, in)
	if err != nil {
		return -1, err
	}
	if opts.Stdout != nil {
		_, _ = io.WriteString(opts.Stdout, out)
	}
	return code, nil
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
			if att := f.attachedTo(name); len(att) > 0 {
				return fmt.Errorf("network %s has active endpoints: %v", name, att)
			}
			delete(f.networks, name)
			f.record("network-remove:" + name)
			return nil
		}
	}
	return nil
}

// ConnectNetwork implements docker.Engine.
func (f *Fake) ConnectNetwork(_ context.Context, network, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	n, ok := f.networks[network]
	if !ok {
		return docker.ErrNotFound
	}
	if !n.Managed {
		return fmt.Errorf("network %s: %w", network, docker.ErrNotManaged)
	}
	c, ok := f.find(containerID)
	if !ok {
		return docker.ErrNotFound
	}
	for _, a := range c.Attached {
		if a == network {
			return nil
		}
	}
	c.Attached = append(c.Attached, network)
	f.record("network-connect:" + network + ":" + c.Spec.Name)
	return nil
}

// DisconnectNetwork implements docker.Engine.
func (f *Fake) DisconnectNetwork(_ context.Context, network, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, ok := f.find(containerID)
	if !ok {
		return nil
	}
	var kept []string
	for _, a := range c.Attached {
		if a != network {
			kept = append(kept, a)
		}
	}
	c.Attached = kept
	f.record("network-disconnect:" + network + ":" + c.Spec.Name)
	return nil
}

// NetworkEndpoints implements docker.Engine. Like RemoveNetwork it counts every attached
// container, running or not.
func (f *Fake) NetworkEndpoints(_ context.Context, network string) ([]docker.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	var out []docker.Endpoint
	for _, c := range f.containers {
		if c.Spec.Network == network || slices.Contains(c.Attached, network) {
			out = append(out, docker.Endpoint{ContainerID: c.ID, Name: c.Spec.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ContainerNetworks implements docker.Engine.
func (f *Fake) ContainerNetworks(_ context.Context, containerID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.find(containerID)
	if !ok {
		return nil, docker.ErrNotFound
	}
	out := append([]string{}, c.Attached...)
	if c.Spec.Network != "" {
		out = append(out, c.Spec.Network)
	}
	return out, nil
}

// NetworkAccess implements docker.Engine.
func (f *Fake) NetworkAccess(_ context.Context, containerID string) (docker.NetworkAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.find(containerID); !ok {
		return docker.NetworkAccess{}, docker.ErrNotFound
	}
	return f.Access, nil
}

// PortBindings implements docker.Engine.
func (f *Fake) PortBindings(_ context.Context, containerID string) ([]docker.PortMapping, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.find(containerID)
	if !ok {
		return nil, docker.ErrNotFound
	}
	var out []docker.PortMapping
	for _, p := range c.Spec.Ports {
		out = append(out, docker.PortMapping{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: "tcp"})
	}
	return out, nil
}

// RemoveNetwork guard: attached (non-project) containers count as endpoints.
func (f *Fake) attachedTo(network string) []string {
	var names []string
	for _, c := range f.containers {
		for _, a := range c.Attached {
			if a == network {
				names = append(names, c.Spec.Name)
			}
		}
	}
	return names
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
	_, ok := f.resolveImage(ref)
	return ok, nil
}

// ImageID implements docker.Engine.
func (f *Fake) ImageID(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", err
	}
	id, ok := f.resolveImage(ref)
	if !ok {
		return "", docker.ErrNotFound
	}
	return id, nil
}

// ListImages implements docker.Engine.
func (f *Fake) ListImages(_ context.Context) ([]docker.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	byID := map[string]*docker.Image{}
	for ref, id := range f.images {
		img, ok := byID[id]
		if !ok {
			img = &docker.Image{ID: id, Size: 100 << 20}
			byID[id] = img
		}
		img.Tags = append(img.Tags, ref)
	}
	for id := range f.dangling {
		byID[id] = &docker.Image{ID: id, Size: 100 << 20, Tags: []string{}}
	}
	out := make([]docker.Image, 0, len(byID))
	for _, img := range byID {
		sort.Strings(img.Tags)
		out = append(out, *img)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// RemoveImage implements docker.Engine.
func (f *Fake) RemoveImage(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	for _, c := range f.containers {
		if c.ImageID == id {
			return fmt.Errorf("conflict: image %s is being used by container %s", id, c.Spec.Name)
		}
	}
	removed := false
	for ref, iid := range f.images {
		if iid == id {
			delete(f.images, ref)
			removed = true
		}
	}
	if f.dangling[id] {
		delete(f.dangling, id)
		removed = true
	}
	if !removed {
		return docker.ErrNotFound
	}
	f.record("image-remove:" + id)
	return nil
}

// EnsureImage implements docker.Engine.
func (f *Fake) EnsureImage(ctx context.Context, ref string, progress docker.PullProgress) error {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return err
	}
	if _, ok := f.resolveImage(ref); ok {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	return f.PullImage(ctx, ref, progress)
}

// TagImage implements docker.Engine.
func (f *Fake) TagImage(_ context.Context, image, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	id, ok := f.resolveImage(image)
	if !ok {
		return docker.ErrNotFound
	}
	if old, ok := f.images[ref]; ok && old != id {
		f.images[ref] = id
		f.dropIfUnreferenced(old, true)
	}
	f.images[ref] = id
	delete(f.dangling, id)
	f.record("tag:" + ref)
	return nil
}

// UntagImage implements docker.Engine.
func (f *Fake) UntagImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	id, ok := f.images[ref]
	if !ok {
		return nil
	}
	delete(f.images, ref)
	f.dropIfUnreferenced(id, false)
	f.record("untag:" + ref)
	return nil
}

// dropIfUnreferenced mirrors Docker after an image lost a tag: still tagged elsewhere →
// nothing; used by a container (or keep) → stays as a dangling image; otherwise gone.
func (f *Fake) dropIfUnreferenced(id string, keep bool) {
	for _, iid := range f.images {
		if iid == id {
			return
		}
	}
	for _, c := range f.containers {
		if c.ImageID == id {
			keep = true
		}
	}
	if keep {
		f.dangling[id] = true
	} else {
		delete(f.dangling, id)
	}
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
	// Like the real engine: the first progress line arrives before any bytes do.
	if progress != nil {
		progress("contacting the registry")
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	f.mu.Lock()
	// Like Docker: the previous id of a re-pulled tag stays on disk without a tag.
	if old, ok := f.images[ref]; ok && old != f.remoteID(ref) {
		stillTagged := false
		for r, id := range f.images {
			if r != ref && id == old {
				stillTagged = true
			}
		}
		if !stillTagged {
			f.dangling[old] = true
		}
	}
	f.images[ref] = f.remoteID(ref)
	f.record("pull:" + ref)
	f.mu.Unlock()
	return nil
}

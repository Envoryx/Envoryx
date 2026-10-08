// Package dockertest provides an in-memory Engine implementation for tests.
package dockertest

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/netip"
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
	// Aliases per attached network.
	Aliases map[string][]string
	// OOMKilled is what inspect reports for the last exit.
	OOMKilled bool
}

// Fake is an in-memory Engine with failure injection.
type Fake struct {
	mu sync.Mutex

	// HostPorts are the ports programs on the fake Docker host listen on.
	HostPorts map[int]bool

	seq        int
	containers map[string]*FakeContainer
	networks   map[string]docker.Network
	volumes    map[string]docker.Volume
	images     map[string]string // ref -> image id
	dangling   map[string]bool   // image ids that lost their tag to a re-pull but still exist
	// StatsByName overrides ContainerStats for a container name.
	StatsByName map[string]docker.Stats
	// statsCalls and listedStatsCalls count ContainerStats and ListedContainerStats.
	statsCalls, listedStatsCalls int
	// VolumeBytes are the sizes VolumeSizes reports.
	VolumeBytes map[string]int64
	// oomWatchers receive EmitOOM events.
	oomWatchers []chan docker.OOMEvent
	// Remote maps image refs to the id a pull would deliver. Unset refs pull as "<ref>@v1".
	Remote map[string]string
	// ImageInfos answers InspectImage per reference; a present image without an entry has
	// an empty configuration.
	ImageInfos map[string]docker.ImageInfo
	// BuildHandler simulates builds: it gets the options and the context tar and may write
	// output or fail. nil means every build succeeds.
	BuildHandler func(opts docker.BuildOptions, context []byte) error
	// Builds records every build (the Context field is drained).
	Builds []docker.BuildOptions
	// Credentials is what SetRegistryCredentials installed.
	Credentials docker.RegistryCredentials
	// KernelVersion is what Ping reports as the host kernel.
	KernelVersion string
	// Access is returned by NetworkAccess for any container; the zero value is bridge
	// networking.
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

	// OneShotHandler simulates transient containers (RunOneShot). nil means exit 0 and no
	// output.
	OneShotHandler func(spec docker.ContainerSpec) (docker.ExecResult, error)
	// OneShotStreamHandler simulates streamed transient containers (RunOneShotStream): it
	// receives the spec and the whole stdin. nil means exit 0 and no output.
	OneShotStreamHandler func(spec docker.ContainerSpec, stdin []byte) (stdout string, code int, err error)
	// OneShots records every RunOneShot spec.
	OneShots []docker.ContainerSpec

	// StreamHandler simulates streamed execs: it receives the container name, argv and the
	// full stdin and returns stdout content plus exit code. nil means exit 0 and no output.
	StreamHandler func(container string, cmd []string, env []string, stdin []byte) (stdout string, code int, err error)
	// NetworkGateways and NetworkIPs answer NetworkAddresses: gateway per network, and
	// address per network and container name.
	NetworkGateways map[string]string
	NetworkIPs      map[string]map[string]string

	// StreamStderr, when set, supplies what a streamed command writes to stderr.
	StreamStderr func(container string, cmd []string) string

	// ReadsStdin tells the fake whether a streamed command consumes stdin to EOF (the
	// default). Commands that ignore stdin must not block on it, just as with the real engine.
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

	// Instance is the Envoryx instance the fake works for, as docker.Options.Instance
	// for the real engine: it is stamped on everything created, and resources labelled
	// with another instance count as unmanaged.
	Instance string
}

// InstanceID implements docker.Engine.
func (f *Fake) InstanceID() string { return f.Instance }

// owns reports whether a resource with these labels is the fake's instance's to manage.
func (f *Fake) owns(labels map[string]string) bool { return docker.Owns(f.Instance, labels) }

// foreign reports whether another Envoryx instance manages a resource with these labels.
func (f *Fake) foreign(labels map[string]string) bool {
	return docker.IsManaged(labels) && !f.owns(labels)
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
		ImageInfos: map[string]docker.ImageInfo{},
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

// AddManagedContainer simulates an Envoryx container that already exists (e.g. after a restart).
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

// AddNetworkWithSubnet adds a network (foreign or managed) that holds an address range.
func (f *Fake) AddNetworkWithSubnet(name string, labels map[string]string, subnet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[name] = docker.Network{ID: f.nextID("n"), Name: name, Driver: "bridge", Labels: labels, Managed: docker.IsManaged(labels), Subnets: []string{subnet}}
}

// AddVolume simulates an existing volume (managed or not, of this or another instance).
func (f *Fake) AddVolume(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[name] = docker.Volume{Name: name, Driver: "local", Labels: labels, Managed: docker.IsManaged(labels)}
}

// NetworkSubnets returns the subnets of a network by name.
func (f *Fake) NetworkSubnets(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.networks[name].Subnets
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

// Dangle removes a tag but keeps its image as dangling, the state an older Envoryx (no
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

// SetLabel changes a label of an existing container, e.g. to make it look like one an
// older Envoryx created with another spec.
func (f *Fake) SetLabel(name, key, value string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.Spec.Name == name {
			labels := map[string]string{}
			for k, v := range c.Spec.Labels {
				labels[k] = v
			}
			labels[key] = value
			c.Spec.Labels = labels
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
	if !f.owns(c.Spec.Labels) {
		return nil, fmt.Errorf("container %s: %w", idOrName, docker.ErrNotManaged)
	}
	return c, nil
}

// toContainer builds the listing view. Like Docker's ContainerList (daemon/list.go,
// refreshImage), Image is the reference given at creation unless that reference no longer
// resolves to the container's image id; then it is the id itself.
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
		Managed: f.owns(c.Spec.Labels),
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
	return docker.Info{APIVersion: "1.56", ServerVersion: "fake", Hostname: "fakehost", OS: "linux", Architecture: "x86_64", Containers: len(f.containers), Running: running, NCPU: 4, MemTotal: 8 << 30, KernelVersion: f.KernelVersion}, nil
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
		managed := f.owns(c.Spec.Labels)
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
	d := docker.ContainerDetails{Container: f.toContainer(c), Running: c.State == "running", Env: c.Spec.Env, Image: c.Spec.Image, OOMKilled: c.OOMKilled}
	if c.Spec.Resources != nil {
		d.Resources = *c.Spec.Resources
	}
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
	spec.Labels = docker.StampInstance(f.Instance, spec.Labels)
	for _, c := range f.containers {
		if c.Spec.Name == spec.Name {
			if f.foreign(c.Spec.Labels) {
				return "", fmt.Errorf("container %s: %w", spec.Name, docker.ErrOtherInstance)
			}
			return "", fmt.Errorf("conflict: container name %q already in use", spec.Name)
		}
	}
	// Docker refuses two mounts on one target.
	targets := map[string]bool{}
	for _, mt := range spec.Mounts {
		if targets[mt.Target] {
			return "", fmt.Errorf("duplicate mount point: %s", mt.Target)
		}
		targets[mt.Target] = true
	}
	// "host" is Docker's own network mode, always there.
	if spec.Network != "" && spec.Network != "host" {
		if _, ok := f.networks[spec.Network]; !ok {
			return "", fmt.Errorf("network %s: %w", spec.Network, docker.ErrNotFound)
		}
	}
	for _, m := range spec.Mounts {
		if m.Type == "volume" {
			v, ok := f.volumes[m.Source]
			if !ok {
				return "", fmt.Errorf("volume %s: %w", m.Source, docker.ErrNotFound)
			}
			if f.foreign(v.Labels) {
				return "", fmt.Errorf("volume %s: %w", m.Source, docker.ErrOtherInstance)
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
	if !f.owns(c.Spec.Labels) {
		return fmt.Errorf("container %s: %w", id, docker.ErrNotManaged)
	}
	delete(f.containers, c.ID)
	f.record("remove:" + c.Spec.Name)
	return nil
}

// UpdateResources implements docker.Engine with the real engine's rule: a limit can be
// changed in place but not lifted.
func (f *Fake) UpdateResources(_ context.Context, id string, r docker.Resources) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	c, err := f.guard(id)
	if err != nil {
		return err
	}
	cur := docker.Resources{}
	if c.Spec.Resources != nil {
		cur = *c.Spec.Resources
	}
	if (r.MemoryBytes == 0 && cur.MemoryBytes != 0) || (r.NanoCPUs == 0 && cur.NanoCPUs != 0) {
		return docker.ErrNeedsRecreate
	}
	next := r
	c.Spec.Resources = &next
	f.record("update-resources:" + c.Spec.Name)
	return nil
}

// WatchOOM implements docker.Engine: EmitOOM delivers events to the watchers.
func (f *Fake) WatchOOM(ctx context.Context, fn func(docker.OOMEvent)) error {
	f.mu.Lock()
	ch := make(chan docker.OOMEvent, 16)
	f.oomWatchers = append(f.oomWatchers, ch)
	f.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-ch:
			fn(ev)
		}
	}
}

// EmitOOM reports an OOM kill in the container with this name to every watcher.
func (f *Fake) EmitOOM(name string) {
	f.mu.Lock()
	var ev docker.OOMEvent
	for _, c := range f.containers {
		if c.Spec.Name == name {
			ev = docker.OOMEvent{ContainerID: c.ID, Name: name, Labels: c.Spec.Labels, Time: time.Now()}
		}
	}
	if f.foreign(ev.Labels) {
		f.mu.Unlock()
		return // the real engine only reports its own instance's containers
	}
	watchers := append([]chan docker.OOMEvent(nil), f.oomWatchers...)
	f.mu.Unlock()
	for _, ch := range watchers {
		ch <- ev
	}
}

// OOMWatchers reports how many WatchOOM calls are listening.
func (f *Fake) OOMWatchers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.oomWatchers)
}

// ContainerStats implements docker.Engine.
func (f *Fake) ContainerStats(_ context.Context, id string) (docker.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsCalls++
	if err := f.check(); err != nil {
		return docker.Stats{}, err
	}
	c, err := f.guard(id)
	if err != nil {
		return docker.Stats{}, err
	}
	return f.stats(c), nil
}

// ListedContainerStats implements docker.Engine: like Docker it trusts the listed labels.
func (f *Fake) ListedContainerStats(_ context.Context, lc docker.Container) (docker.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedStatsCalls++
	if err := f.check(); err != nil {
		return docker.Stats{}, err
	}
	if !f.owns(lc.Labels) {
		return docker.Stats{}, fmt.Errorf("container %s: %w", lc.ID, docker.ErrNotManaged)
	}
	c, ok := f.containers[lc.ID]
	if !ok {
		return docker.Stats{}, docker.ErrNotFound
	}
	return f.stats(c), nil
}

// StatsCalls reports how often ContainerStats and ListedContainerStats were called.
func (f *Fake) StatsCalls() (guarded, listed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsCalls, f.listedStatsCalls
}

func (f *Fake) stats(c *FakeContainer) docker.Stats {
	if c.State != "running" {
		return docker.Stats{ContainerID: c.ID, SampledAt: time.Now().UTC()}
	}
	if st, ok := f.StatsByName[c.Spec.Name]; ok {
		st.ContainerID, st.SampledAt = c.ID, time.Now().UTC()
		return st
	}
	return docker.Stats{ContainerID: c.ID, CPUPercent: 1.5, MemoryBytes: 32 << 20, MemoryLimit: 8 << 30, SampledAt: time.Now().UTC()}
}

// VolumeSizes implements docker.Engine: the managed volumes with the sizes set in
// VolumeBytes (0 otherwise).
func (f *Fake) VolumeSizes(context.Context) ([]docker.VolumeSize, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	var out []docker.VolumeSize
	for name, v := range f.volumes {
		if f.owns(v.Labels) {
			out = append(out, docker.VolumeSize{Name: name, Labels: v.Labels, Bytes: f.VolumeBytes[name]})
		}
	}
	return out, nil
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
	spec.Labels = docker.StampInstance(f.Instance, spec.Labels)
	f.OneShots = append(f.OneShots, spec)
	f.record("oneshot:" + spec.Name)
	handler := f.OneShotHandler
	f.mu.Unlock()
	if handler != nil {
		return handler(spec)
	}
	return docker.ExecResult{}, nil
}

// RunOneShotStream implements docker.Engine. Specs are recorded in OneShots.
func (f *Fake) RunOneShotStream(_ context.Context, spec docker.ContainerSpec, opts docker.ExecStreamOptions) (int, error) {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return -1, err
	}
	if !docker.IsManaged(spec.Labels) {
		f.mu.Unlock()
		return -1, docker.ErrNotManaged
	}
	spec.OpenStdin = opts.Stdin != nil
	spec.Labels = docker.StampInstance(f.Instance, spec.Labels)
	f.OneShots = append(f.OneShots, spec)
	f.record("oneshot:" + spec.Name)
	handler := f.OneShotStreamHandler
	f.mu.Unlock()
	var in []byte
	if opts.Stdin != nil {
		in, _ = io.ReadAll(opts.Stdin)
	}
	if handler == nil {
		return 0, nil
	}
	out, code, err := handler(spec, in)
	if err != nil {
		return -1, err
	}
	if opts.Stdout != nil {
		_, _ = io.WriteString(opts.Stdout, out)
	}
	return code, nil
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
	handler, reads, stderr := f.StreamHandler, f.ReadsStdin, f.StreamStderr
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
	if stderr != nil && opts.Stderr != nil {
		_, _ = io.WriteString(opts.Stderr, stderr(name, opts.Cmd))
	}
	return code, nil
}

// StreamLogs implements docker.Engine. With Follow it emits the configured lines and then
// keeps polling for lines added through AppendLogs (or replaced through SetLogs) until ctx
// is cancelled or the container stops running or is removed. Since and Until filter like
// Docker does (both inclusive).
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
	name := c.Spec.Name
	lines := append([]docker.LogLine(nil), f.Logs[name]...)
	f.mu.Unlock()
	keep := func(l docker.LogLine) bool {
		return (opts.Since.IsZero() || !l.Time.Before(opts.Since)) && (opts.Until.IsZero() || !l.Time.After(opts.Until))
	}
	sent := len(lines)
	filtered := lines[:0:0]
	for _, l := range lines {
		if keep(l) {
			filtered = append(filtered, l)
		}
	}
	lines = filtered
	if opts.Tail != "" && opts.Tail != "all" {
		if n, err := strconv.Atoi(opts.Tail); err == nil && n < len(lines) {
			lines = lines[len(lines)-n:]
		}
	}
	for _, l := range lines {
		emit(l)
	}
	if !opts.Follow {
		return nil
	}
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		f.mu.Lock()
		cur, ok := f.containers[c.ID]
		if !ok {
			// Removed: Docker ends the stream, and output under this name now belongs
			// to a successor.
			f.mu.Unlock()
			return nil
		}
		all := f.Logs[name]
		if len(all) < sent {
			sent = 0 // SetLogs replaced the output, as a recreated container starts over
		}
		more := append([]docker.LogLine(nil), all[sent:]...)
		sent = len(all)
		running := cur.State == "running"
		f.mu.Unlock()
		for _, l := range more {
			if keep(l) {
				emit(l)
			}
		}
		if !running {
			return nil
		}
	}
}

// SetLogs replaces a container's output (by name), as a recreated container starts over.
func (f *Fake) SetLogs(name string, lines ...docker.LogLine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Logs[name] = lines
}

// AppendLogs adds output to a container (by name); a following StreamLogs picks it up.
func (f *Fake) AppendLogs(name string, lines ...docker.LogLine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Logs[name] = append(f.Logs[name], lines...)
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
		n.Managed = f.owns(n.Labels)
		if managedOnly && !n.Managed {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateNetwork implements docker.Engine.
func (f *Fake) CreateNetwork(_ context.Context, name string, labels map[string]string, subnet string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", err
	}
	if !docker.IsManaged(labels) {
		return "", docker.ErrNotManaged
	}
	if n, ok := f.networks[name]; ok {
		if f.foreign(n.Labels) {
			return "", fmt.Errorf("network %s: %w", name, docker.ErrOtherInstance)
		}
		return "", fmt.Errorf("network %q already exists", name)
	}
	labels = docker.StampInstance(f.Instance, labels)
	var subnets []string
	if subnet != "" {
		want, err := netip.ParsePrefix(subnet)
		if err != nil {
			return "", err
		}
		// Like Docker: a subnet that overlaps another network's is refused.
		for _, n := range f.networks {
			for _, s := range n.Subnets {
				if have, err := netip.ParsePrefix(s); err == nil && have.Overlaps(want) {
					return "", fmt.Errorf("%w: Pool overlaps with other one on this address space", docker.ErrSubnetInUse)
				}
			}
		}
		subnets = []string{subnet}
	}
	id := f.nextID("n")
	f.networks[name] = docker.Network{ID: id, Name: name, Driver: "bridge", Labels: labels, Managed: true, Subnets: subnets}
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
			if !f.owns(n.Labels) {
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
func (f *Fake) ConnectNetwork(_ context.Context, network, containerID string, aliases ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	n, ok := f.networks[network]
	if !ok {
		return docker.ErrNotFound
	}
	if !f.owns(n.Labels) {
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
	if len(aliases) > 0 {
		if c.Aliases == nil {
			c.Aliases = map[string][]string{}
		}
		c.Aliases[network] = append([]string{}, aliases...)
	}
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
	delete(c.Aliases, network)
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

// NetworkAddresses implements docker.Engine.
func (f *Fake) NetworkAddresses(_ context.Context, network string) (string, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return "", nil, err
	}
	ips := map[string]string{}
	for _, c := range f.containers {
		if ip, ok := f.NetworkIPs[network][c.Spec.Name]; ok {
			ips[c.ID] = ip
		}
	}
	return f.NetworkGateways[network], ips, nil
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

// NetworkAliases implements docker.Engine.
func (f *Fake) NetworkAliases(_ context.Context, network, containerID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.find(containerID)
	if !ok {
		return nil, docker.ErrNotFound
	}
	return append([]string(nil), c.Aliases[network]...), nil
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

// attachedTo names the containers joined to network through ConnectNetwork. RemoveNetwork
// counts them as endpoints, like the containers created on the network.
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
		v.Managed = f.owns(v.Labels)
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
	if v, ok := f.volumes[name]; ok && f.foreign(v.Labels) {
		return fmt.Errorf("volume %s: %w", name, docker.ErrOtherInstance)
	}
	f.volumes[name] = docker.Volume{Name: name, Driver: "local", Labels: docker.StampInstance(f.Instance, labels), Managed: true}
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
	if !f.owns(v.Labels) {
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

// InspectImage implements docker.Engine.
func (f *Fake) InspectImage(_ context.Context, ref string) (docker.ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return docker.ImageInfo{}, err
	}
	id, ok := f.resolveImage(ref)
	if !ok {
		return docker.ImageInfo{}, docker.ErrNotFound
	}
	info := f.ImageInfos[ref]
	info.ID = id
	return info, nil
}

// BuildImage implements docker.Engine.
func (f *Fake) BuildImage(_ context.Context, opts docker.BuildOptions) error {
	f.mu.Lock()
	if err := f.check(); err != nil {
		f.mu.Unlock()
		return err
	}
	handler := f.BuildHandler
	f.mu.Unlock()
	var tarball []byte
	if opts.Context != nil {
		b, err := io.ReadAll(opts.Context)
		if err != nil {
			return err
		}
		tarball = b
	}
	if handler != nil {
		if err := handler(opts, tarball); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	opts.Context = nil
	f.Builds = append(f.Builds, opts)
	if old, ok := f.images[opts.Tag]; ok {
		f.images[opts.Tag] = ""
		f.dropIfUnreferenced(old, false)
	}
	f.images[opts.Tag] = f.nextID("sha256:")
	f.record("build:" + opts.Tag)
	return nil
}

// SetRegistryCredentials implements docker.Engine.
func (f *Fake) SetRegistryCredentials(creds docker.RegistryCredentials) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Credentials = creds
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

// HostListeningPorts implements docker.HostPortLister.
func (f *Fake) HostListeningPorts(_ context.Context) (map[int]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.HostPorts), nil
}

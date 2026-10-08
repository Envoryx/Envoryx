// Package docker is the only package that talks to the Docker Engine. Its Engine
// interface is deliberately small and only touches resources labelled as Envoryx's, so
// no other code can run arbitrary Docker operations.
package docker

import (
	"context"
	"errors"
	"io"
	"maps"
	"regexp"
	"strings"
	"time"
)

// Labels used to mark and identify Envoryx-managed resources.
const (
	LabelManaged     = "envoryx.managed"
	LabelProjectID   = "envoryx.project.id"
	LabelProjectName = "envoryx.project.name"
	LabelService     = "envoryx.service"
	// LabelSpec is a fingerprint of the container spec (command, environment, mounts, ports);
	// a mismatch tells Envoryx to recreate the container.
	LabelSpec    = "envoryx.spec"
	LabelVersion = "envoryx.version"
	// LabelSystem marks instance-wide helper resources that belong to no project (the
	// database browser); they are managed but never orphans.
	LabelSystem = "envoryx.system"
	// LabelInstance names the Envoryx instance that created the resource. Several
	// instances can share one Docker host; each leaves alone what another one labelled
	// (see Owns).
	LabelInstance = "envoryx.instance"
)

// Labels read outside Envoryx: the Unraid Docker page shows the icon, and the FolderView3
// plugin puts a container into the folder its label names (the folder must exist there).
const (
	LabelUnraidIcon = "net.unraid.docker.icon"
	LabelFolderView = "folder.view3"
	// UnraidIcon is the icon shown for every container Envoryx creates.
	UnraidIcon = "https://raw.githubusercontent.com/envoryx/envoryx/main/deploy/unraid/envoryx.png"
)

// ErrNotManaged is returned when an operation targets a resource without the managed label.
var ErrNotManaged = errors.New("resource is not managed by Envoryx")

// ErrOtherInstance is returned when a name Envoryx wants to create or mount belongs to a
// resource of another Envoryx instance on the same Docker host.
var ErrOtherInstance = errors.New("the name is taken by another Envoryx instance on this Docker host")

// ErrNotFound is returned when a resource does not exist.
var ErrNotFound = errors.New("docker resource not found")

// ErrUnavailable is returned when the Docker Engine cannot be reached.
var ErrUnavailable = errors.New("docker engine unavailable")

// ErrPortInUse is returned when a container cannot publish a host port because something
// else on the Docker host holds it.
var ErrPortInUse = errors.New("host port in use")

// ErrNoGPU is returned when a container asks for GPUs Docker cannot hand over.
// ErrSubnetInUse means the subnet asked for overlaps a network or route Docker already has.
var ErrSubnetInUse = errors.New("the subnet overlaps an existing network")

// ErrAddressPoolsExhausted means Docker had no address range left for a new network.
var ErrAddressPoolsExhausted = errors.New("Docker has no free address range left for a new network")

var ErrNoGPU = errors.New("Docker cannot hand GPUs to containers; install the NVIDIA Container Toolkit (on Unraid: the Nvidia Driver plugin) and restart Docker")

// Info describes the connected Docker Engine.
type Info struct {
	APIVersion    string
	ServerVersion string
	// Hostname is the daemon's host name (docker info "Name"), e.g. the Unraid server.
	Hostname     string
	OS           string
	Architecture string
	Containers   int
	Running      int
	NCPU         int
	MemTotal     int64
	// KernelVersion is the Docker host's kernel release (uname -r), e.g.
	// "6.19.0-31-generic"; containers share it, so it decides what they can run.
	KernelVersion string
}

// Container is a summary of a container as listed by the engine.
type Container struct {
	ID      string
	Name    string
	Image   string
	ImageID string // content-addressable id of the image the container was created from
	State   string // created, running, paused, restarting, removing, exited, dead
	Status  string
	Created time.Time
	Labels  map[string]string
	Ports   []PortMapping
	Managed bool
	// Health is "healthy", "unhealthy", "starting" or "" when the image defines no check.
	Health string
}

// ProjectID returns the project label of a container.
func (c Container) ProjectID() string { return c.Labels[LabelProjectID] }

// Service returns the service label of a container.
func (c Container) Service() string { return c.Labels[LabelService] }

// PortMapping is a published port.
type PortMapping struct {
	HostIP        string
	HostPort      int
	ContainerPort int
	Protocol      string
}

// ContainerDetails is the inspect view used by Envoryx.
type ContainerDetails struct {
	Container
	Running    bool
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Mounts     []MountPoint
	Networks   []string
	Env        []string
	Image      string
	// Resources are the limits the container runs with.
	Resources Resources
	// OOMKilled reports that the main process was last ended by the OOM killer.
	OOMKilled bool
}

// MountPoint describes a mount of a running container.
type MountPoint struct {
	Type        string
	Source      string
	Destination string
	ReadOnly    bool
}

// Network summarises a Docker network.
type Network struct {
	ID      string
	Name    string
	Driver  string
	Labels  map[string]string
	Managed bool
	// Subnets are the network's IPv4 and IPv6 prefixes (e.g. 10.213.0.0/24).
	Subnets []string
}

// Endpoint is a container attached to a network.
type Endpoint struct {
	ContainerID string
	Name        string
}

// NetworkAccess describes the reachability of a container's ports.
type NetworkAccess struct {
	// Mode is the container's network mode (bridge, host, <network name> …).
	Mode string
	// Direct is true when container ports are reachable without port publishing:
	// host networking, or an own IP on a macvlan/ipvlan network.
	Direct bool
	// IPs are the container's addresses on directly reachable networks.
	IPs []string
}

// Volume summarises a Docker volume.
type Volume struct {
	Name    string
	Driver  string
	Labels  map[string]string
	Managed bool
}

// MountSpec is a mount requested by the planner. Only bind mounts of paths derived from
// the projects/config roots and named volumes are ever produced.
type MountSpec struct {
	Type     string // "bind" or "volume"
	Source   string // host path or volume name
	Target   string
	ReadOnly bool
}

// PortSpec publishes a container port on the host.
type PortSpec struct {
	HostIP        string // empty means all interfaces
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp"
}

// HealthSpec configures a container health check (exec form, no shell).
type HealthSpec struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

// ContainerSpec is the closed set of parameters Envoryx uses to create containers.
// Privileged mode, capability additions, host networking, device access and arbitrary
// binds are intentionally not representable; the one exception is GPUs.
type ContainerSpec struct {
	Name   string
	Image  string
	Labels map[string]string
	Env    []string
	Cmd    []string
	// Entrypoint overrides the image's; only one-shot helpers need it (an image whose
	// entrypoint is its own server cannot run a copy command otherwise).
	Entrypoint    []string
	WorkingDir    string
	User          string
	Mounts        []MountSpec
	Ports         []PortSpec
	Network       string
	NetworkAlias  []string
	RestartPolicy string // "unless-stopped" | "no"
	StopTimeout   int    // seconds
	ExtraHosts    []string
	Healthcheck   *HealthSpec
	// Resources caps CPU, memory and processes; nil means no limits. They aren't part of
	// the spec fingerprint, because UpdateResources changes them on a running container.
	Resources *Resources
	// OpenStdin keeps the process's stdin open for one attached client (RunOneShotStream
	// sets it when it has input to feed).
	OpenStdin bool
	// GPUs hands every GPU to the container, as `docker run --gpus all` does. Docker
	// needs the NVIDIA Container Toolkit (or a CDI spec) for it, otherwise creating the
	// container fails.
	GPUs bool
}

// Resources are the limits of one container. Zero means no limit (PIDs: Docker's
// default, unlimited).
type Resources struct {
	// NanoCPUs is the CPU quota in billionths of a core (1.5 cores = 1_500_000_000).
	NanoCPUs int64
	// MemoryBytes is the hard memory limit; swap is not added on top.
	MemoryBytes int64
	// PidsLimit caps the number of processes and threads.
	PidsLimit int64
}

// ErrNeedsRecreate is returned by UpdateResources when a limit is to be lifted
// entirely, which Docker only allows on a new container.
var ErrNeedsRecreate = errors.New("the container must be recreated to lift a limit")

// OOMEvent reports that the kernel killed a process of a managed container for running
// out of its memory limit (the container itself may keep running).
type OOMEvent struct {
	ContainerID string
	Name        string
	Labels      map[string]string
	Time        time.Time
}

// TerminalOptions configure an interactive exec session.
type TerminalOptions struct {
	Cmd        []string
	Env        []string
	User       string
	WorkingDir string
	Cols, Rows uint
}

// Terminal is an interactive exec session with a pseudo terminal.
type Terminal interface {
	// Output delivers raw terminal output.
	Output() io.Reader
	// Input receives keystrokes.
	Input() io.Writer
	// Resize changes the pseudo terminal size.
	Resize(ctx context.Context, cols, rows uint) error
	// ExitCode returns the exit code once the process has finished (-1 while running).
	ExitCode(ctx context.Context) (int, error)
	// Close terminates the session.
	Close() error
}

// ExecStreamOptions configure a streamed exec.
type ExecStreamOptions struct {
	Cmd        []string
	Env        []string
	User       string
	WorkingDir string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// ExecResult is the outcome of a non-interactive command run inside a container.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Image summarises a local image.
type Image struct {
	ID      string
	Tags    []string
	Size    int64
	Created time.Time
}

// Stats is a single resource usage sample.
type Stats struct {
	ContainerID string
	CPUPercent  float64
	MemoryBytes int64
	MemoryLimit int64
	// Cumulative counters since the container started: bytes received and sent on all
	// its networks, bytes read from and written to block devices.
	NetRxBytes   uint64
	NetTxBytes   uint64
	BlockRead    uint64
	BlockWritten uint64
	SampledAt    time.Time
}

// VolumeSize is the disk space a managed volume takes.
type VolumeSize struct {
	Name   string
	Labels map[string]string
	// Bytes is -1 when Docker could not determine it.
	Bytes int64
}

// LogLine is one line of container output.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"` // stdout | stderr
	Text   string    `json:"text"`
}

// ansiEscape matches terminal control sequences: colours and cursor moves (CSI), window
// titles and links (OSC) and character set switches.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]`)

// StripANSI removes terminal control sequences from a log line. Tools like Caddy colour
// their output when they think they write to a terminal; the log views show plain text,
// and the codes would also get in the way of search and level detection.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiEscape.ReplaceAllString(s, "")
}

// LogOptions control log streaming.
type LogOptions struct {
	// Tail limits the initial history; empty or "all" returns everything.
	Tail string
	// Follow keeps streaming until ctx is cancelled.
	Follow bool
	// Since only returns lines newer than this time; the zero time means no limit.
	Since time.Time
	// Until only returns lines older than this time; the zero time means no limit.
	Until time.Time
}

// PullProgress receives human readable image pull progress lines.
type PullProgress func(msg string)

// RegistryCredential is a login for a private registry, used for pulls and for the base
// images of builds. Host is the registry's host name ("docker.io" for Docker Hub).
type RegistryCredential struct {
	Host     string
	Username string
	Password string
}

// RegistryCredentials returns the configured registry logins; it is asked before every
// pull and build, so changed settings apply at once.
type RegistryCredentials func() []RegistryCredential

// BuildOptions describe an image build.
type BuildOptions struct {
	// Context is the build context as a tar stream.
	Context io.Reader
	// Dockerfile is the Dockerfile's path inside the context.
	Dockerfile string
	// Tag names the result.
	Tag string
	// Labels are added to the image.
	Labels map[string]string
	// Pull fetches newer versions of the base images; NoCache ignores the build cache.
	Pull    bool
	NoCache bool
	// Output receives the build output line by line.
	Output func(line string)
}

// ImageInfo is what Envoryx reads from an image's configuration.
type ImageInfo struct {
	ID         string
	Labels     map[string]string
	Entrypoint []string
	Cmd        []string
	User       string
}

// Engine is the label-scoped Docker abstraction used by Envoryx. "Managed" means managed
// by this instance (Owns): an engine stamps its instance label on everything it creates,
// and a resource labelled by another instance is as unmanaged to it as any foreign one.
type Engine interface {
	// Ping checks connectivity and returns engine information.
	Ping(ctx context.Context) (Info, error)

	// InstanceID is the Envoryx instance the engine works for (LabelInstance); "" when
	// resources are not labelled.
	InstanceID() string
	// ListContainers lists containers. If managedOnly is true only the containers this
	// instance manages are returned; projectID additionally filters by project.
	ListContainers(ctx context.Context, managedOnly bool, projectID string) ([]Container, error)
	// InspectContainer returns details for a managed container by ID or name.
	InspectContainer(ctx context.Context, idOrName string) (ContainerDetails, error)
	// InspectMounts returns the mounts of any container (read-only, used to discover the
	// host paths behind Envoryx's own /config and /projects mounts).
	InspectMounts(ctx context.Context, idOrName string) ([]MountPoint, error)
	// CreateContainer creates (but does not start) a container from a spec.
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, error)
	// StartContainer starts a managed container.
	StartContainer(ctx context.Context, id string) error
	// StopContainer stops a managed container with a grace period.
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	// RestartContainer restarts a managed container.
	RestartContainer(ctx context.Context, id string, timeout time.Duration) error
	// RemoveContainer force-removes a managed container.
	RemoveContainer(ctx context.Context, id string) error
	// UpdateResources changes the limits of a managed container, running or not. Lifting
	// a CPU or memory limit entirely returns ErrNeedsRecreate.
	UpdateResources(ctx context.Context, id string, r Resources) error
	// WatchOOM reports OOM kills in managed containers until ctx ends or the event stream
	// breaks (the returned error says why; nil when ctx ended).
	WatchOOM(ctx context.Context, fn func(OOMEvent)) error
	// VolumeSizes reports the size of every managed volume. Docker walks the volumes
	// for it, so it is not for frequent use.
	VolumeSizes(ctx context.Context) ([]VolumeSize, error)
	// ContainerStats returns one usage sample of a managed container.
	ContainerStats(ctx context.Context, id string) (Stats, error)
	// ListedContainerStats is ContainerStats for a container ListContainers just returned.
	// It checks the managed label on the listed labels instead of inspecting the container
	// again, which saves a Docker round trip per sample when every running container is
	// sampled.
	ListedContainerStats(ctx context.Context, c Container) (Stats, error)
	// Exec runs a command (argv form, never a shell string) inside a managed container and
	// waits for it to finish. env entries are KEY=VALUE.
	Exec(ctx context.Context, id string, cmd []string, env []string) (ExecResult, error)
	// ExecStream runs a command with streamed stdin/stdout/stderr (for dumps and restores)
	// and returns its exit code. stdin may be nil.
	ExecStream(ctx context.Context, id string, opts ExecStreamOptions) (int, error)
	// RunOneShot creates a transient container from spec, runs it to completion, collects
	// its output and removes it. The spec must carry managed labels.
	RunOneShot(ctx context.Context, spec ContainerSpec) (ExecResult, error)
	// RunOneShotStream is RunOneShot with streams: stdin (may be nil) is fed to the
	// process and its output is written to opts while it runs, like `docker run --rm -i`.
	// Cmd, Env, User and WorkingDir of opts are ignored; the spec carries them.
	RunOneShotStream(ctx context.Context, spec ContainerSpec, opts ExecStreamOptions) (int, error)
	// OpenTerminal starts an interactive shell (PTY) inside a managed container.
	OpenTerminal(ctx context.Context, id string, opts TerminalOptions) (Terminal, error)
	// StreamLogs emits log lines of a managed container until the stream ends (Follow=false)
	// or ctx is cancelled. emit is called from a single goroutine.
	StreamLogs(ctx context.Context, id string, opts LogOptions, emit func(LogLine)) error

	// ListNetworks lists networks; managedOnly restricts to Envoryx networks.
	ListNetworks(ctx context.Context, managedOnly bool) ([]Network, error)
	// CreateNetwork creates a bridge network with labels. subnet (e.g. 10.213.4.0/24) fixes
	// its address range; empty leaves the choice to Docker.
	CreateNetwork(ctx context.Context, name string, labels map[string]string, subnet string) (string, error)
	// RemoveNetwork removes a managed network.
	RemoveNetwork(ctx context.Context, idOrName string) error
	// ConnectNetwork attaches a container to a managed network (used to attach Envoryx's own
	// container so the embedded proxy can reach project web servers). Aliases are extra DNS
	// names of the container on that network; they are fixed until it is reconnected.
	ConnectNetwork(ctx context.Context, network, containerID string, aliases ...string) error
	// DisconnectNetwork detaches a container from a managed network.
	DisconnectNetwork(ctx context.Context, network, containerID string) error
	// ContainerNetworks lists the network names a container is attached to.
	ContainerNetworks(ctx context.Context, containerID string) ([]string, error)
	// NetworkAliases lists the aliases a container was given on a network (nil when it is
	// not attached to it).
	NetworkAliases(ctx context.Context, network, containerID string) ([]string, error)
	// NetworkEndpoints lists the containers currently attached to a network - the ones
	// that would make its removal fail. A missing network yields no endpoints.
	NetworkEndpoints(ctx context.Context, network string) ([]Endpoint, error)
	// NetworkAddresses returns a network's IPv4 gateway and the IPv4 address of every
	// container attached to it, by container ID.
	NetworkAddresses(ctx context.Context, network string) (gateway string, containers map[string]string, err error)
	// PortBindings returns the published ports of any container. Envoryx uses it to find
	// out how its own proxy ports are mapped.
	PortBindings(ctx context.Context, containerID string) ([]PortMapping, error)
	// NetworkAccess describes how a container's listeners are reachable from the LAN:
	// through published ports, or directly because it uses host networking or has its
	// own IP on a macvlan/ipvlan network.
	NetworkAccess(ctx context.Context, containerID string) (NetworkAccess, error)

	// ListVolumes lists volumes; managedOnly restricts to Envoryx volumes.
	ListVolumes(ctx context.Context, managedOnly bool) ([]Volume, error)
	// CreateVolume creates a named local volume with labels. When another instance has a
	// volume of that name it fails with ErrOtherInstance (as CreateContainer does for a
	// name or a volume mount, and CreateNetwork for a name).
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	// RemoveVolume removes a managed volume.
	RemoveVolume(ctx context.Context, name string) error

	// EnsureImage pulls an image if it is not present locally.
	EnsureImage(ctx context.Context, ref string, progress PullProgress) error
	// PullImage always pulls the tag so a rebuilt upstream image replaces the local one.
	PullImage(ctx context.Context, ref string, progress PullProgress) error
	// ImageExists reports whether the image is available locally.
	ImageExists(ctx context.Context, ref string) (bool, error)
	// InspectImage returns the configuration of a local image (ErrNotFound if absent).
	InspectImage(ctx context.Context, ref string) (ImageInfo, error)
	// BuildImage builds and tags an image. A failed step returns an error whose text ends
	// with Docker's message; the output up to it went to opts.Output.
	BuildImage(ctx context.Context, opts BuildOptions) error
	// SetRegistryCredentials installs the lookup for private registry logins.
	SetRegistryCredentials(creds RegistryCredentials)
	// ImageID returns the local id of an image reference (ErrNotFound if absent).
	ImageID(ctx context.Context, ref string) (string, error)
	// ListImages lists local images.
	ListImages(ctx context.Context) ([]Image, error)
	// RemoveImage deletes an image by id. It fails when a container still uses it.
	RemoveImage(ctx context.Context, id string) error
	// TagImage gives the image (id or reference) an additional tag, moving the tag if it
	// already points elsewhere.
	TagImage(ctx context.Context, image, ref string) error
	// UntagImage removes one tag. The image itself is deleted only when nothing else
	// (another tag or a container) references it; a missing tag is not an error.
	UntagImage(ctx context.Context, ref string) error
}

// ManagedLabels builds the standard label set for a project resource.
func ManagedLabels(projectID, projectSlug, service, version string) map[string]string {
	l := map[string]string{
		LabelManaged:     "true",
		LabelProjectID:   projectID,
		LabelProjectName: projectSlug,
		LabelVersion:     version,
	}
	if service != "" {
		l[LabelService] = service
	}
	return l
}

// AddUnraidLabels adds the Unraid icon and, when folder is set, the FolderView3 folder to a
// container's labels.
func AddUnraidLabels(labels map[string]string, folder string) {
	labels[LabelUnraidIcon] = UnraidIcon
	if folder != "" {
		labels[LabelFolderView] = folder
	}
}

// IsManaged reports whether a label set carries the managed marker.
func IsManaged(labels map[string]string) bool {
	return labels[LabelManaged] == "true"
}

// Owns reports whether the given instance manages a resource with these labels: it
// carries the managed marker and either this instance's label or none at all. Resources
// without one were created before instances labelled theirs; they count as the
// instance's own here, and the project code decides by their project whether they are
// (see project.Reconcile). Engines treat everything else as unmanaged, so another
// instance's resources are never listed as managed, changed or removed.
func Owns(instance string, labels map[string]string) bool {
	if !IsManaged(labels) {
		return false
	}
	l := labels[LabelInstance]
	return l == "" || l == instance
}

// StampInstance returns a copy of labels with the instance label set; an empty instance
// leaves them as they are. Engines apply it to every resource they create, so no creation
// path can miss it, and the spec fingerprint, computed from the labels the planner sets,
// stays the same.
func StampInstance(instance string, labels map[string]string) map[string]string {
	if instance == "" {
		return labels
	}
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	out[LabelInstance] = instance
	return out
}

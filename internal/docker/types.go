// Package docker is the only package that talks to the Docker Engine. It exposes a narrow,
// label-scoped Engine interface so that the rest of Envoryx can never issue arbitrary
// Docker operations.
package docker

import (
	"context"
	"errors"
	"io"
	"time"
)

// Labels used to mark and identify Envoryx-managed resources.
const (
	LabelManaged     = "envoryx.managed"
	LabelProjectID   = "envoryx.project.id"
	LabelProjectName = "envoryx.project.name"
	LabelService     = "envoryx.service"
	// LabelSpec is a fingerprint of the structural container spec (command, mounts, ports);
	// a mismatch tells Envoryx to recreate the container.
	LabelSpec    = "envoryx.spec"
	LabelVersion = "envoryx.version"
	// LabelSystem marks instance-wide helper resources that belong to no project (the
	// database browser); they are managed but never orphans.
	LabelSystem = "envoryx.system"
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

// ErrNotFound is returned when a resource does not exist.
var ErrNotFound = errors.New("docker resource not found")

// ErrUnavailable is returned when the Docker Engine cannot be reached.
var ErrUnavailable = errors.New("docker engine unavailable")

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
	HostIP        string // "" = all interfaces
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
// binds are intentionally not representable.
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
	// Resources caps CPU, memory and processes (nil = no limits). They are not part of
	// the spec fingerprint: UpdateResources changes them on a running container.
	Resources *Resources
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
	SampledAt   time.Time
}

// LogLine is one line of container output.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"` // stdout | stderr
	Text   string    `json:"text"`
}

// LogOptions control log streaming.
type LogOptions struct {
	// Tail limits the initial history ("" or "all" = everything).
	Tail string
	// Follow keeps streaming until ctx is cancelled.
	Follow bool
	// Since only returns lines newer than this time (zero = no limit).
	Since time.Time
	// Until only returns lines older than this time (zero = no limit).
	Until time.Time
}

// PullProgress receives human readable image pull progress lines.
type PullProgress func(msg string)

// Engine is the label-scoped Docker abstraction used by Envoryx.
type Engine interface {
	// Ping checks connectivity and returns engine information.
	Ping(ctx context.Context) (Info, error)

	// ListContainers lists containers. If managedOnly is true only envoryx.managed=true
	// containers are returned; projectID additionally filters by project.
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
	// ContainerStats returns one usage sample of a managed container.
	ContainerStats(ctx context.Context, id string) (Stats, error)
	// Exec runs a command (argv form, never a shell string) inside a managed container and
	// waits for it to finish. env entries are KEY=VALUE.
	Exec(ctx context.Context, id string, cmd []string, env []string) (ExecResult, error)
	// ExecStream runs a command with streamed stdin/stdout/stderr (for dumps and restores)
	// and returns its exit code. stdin may be nil.
	ExecStream(ctx context.Context, id string, opts ExecStreamOptions) (int, error)
	// RunOneShot creates a transient container from spec, runs it to completion, collects
	// its output and removes it. The spec must carry managed labels.
	RunOneShot(ctx context.Context, spec ContainerSpec) (ExecResult, error)
	// OpenTerminal starts an interactive shell (PTY) inside a managed container.
	OpenTerminal(ctx context.Context, id string, opts TerminalOptions) (Terminal, error)
	// StreamLogs emits log lines of a managed container until the stream ends (Follow=false)
	// or ctx is cancelled. emit is called from a single goroutine.
	StreamLogs(ctx context.Context, id string, opts LogOptions, emit func(LogLine)) error

	// ListNetworks lists networks; managedOnly restricts to Envoryx networks.
	ListNetworks(ctx context.Context, managedOnly bool) ([]Network, error)
	// CreateNetwork creates a bridge network with labels.
	CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error)
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
	// NetworkEndpoints lists the containers currently attached to a network – the ones
	// that would make its removal fail. A missing network yields no endpoints.
	NetworkEndpoints(ctx context.Context, network string) ([]Endpoint, error)
	// NetworkAddresses returns a network's IPv4 gateway and the IPv4 address of every
	// container attached to it, by container ID.
	NetworkAddresses(ctx context.Context, network string) (gateway string, containers map[string]string, err error)
	// SelfPortBindings returns the host ports published for the given container ports of
	// any container (used to discover how Envoryx's own proxy ports are mapped).
	PortBindings(ctx context.Context, containerID string) ([]PortMapping, error)
	// NetworkAccess describes how a container's listeners are reachable from the LAN:
	// through published ports, or directly because it uses host networking or has its
	// own IP on a macvlan/ipvlan network.
	NetworkAccess(ctx context.Context, containerID string) (NetworkAccess, error)

	// ListVolumes lists volumes; managedOnly restricts to Envoryx volumes.
	ListVolumes(ctx context.Context, managedOnly bool) ([]Volume, error)
	// CreateVolume creates a named local volume with labels.
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	// RemoveVolume removes a managed volume.
	RemoveVolume(ctx context.Context, name string) error

	// EnsureImage pulls an image if it is not present locally.
	EnsureImage(ctx context.Context, ref string, progress PullProgress) error
	// PullImage always pulls the tag so a rebuilt upstream image replaces the local one.
	PullImage(ctx context.Context, ref string, progress PullProgress) error
	// ImageExists reports whether the image is available locally.
	ImageExists(ctx context.Context, ref string) (bool, error)
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

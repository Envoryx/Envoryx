// Package docker is the only package that talks to the Docker Engine. It exposes a narrow,
// label-scoped Engine interface so that the rest of Staqio can never issue arbitrary
// Docker operations.
package docker

import (
	"context"
	"errors"
	"io"
	"time"
)

// Labels used to mark and identify Staqio-managed resources.
const (
	LabelManaged     = "staqio.managed"
	LabelProjectID   = "staqio.project.id"
	LabelProjectName = "staqio.project.name"
	LabelService     = "staqio.service"
	// LabelSpec is a fingerprint of the structural container spec (command, mounts, ports);
	// a mismatch tells Staqio to recreate the container.
	LabelSpec    = "staqio.spec"
	LabelVersion = "staqio.version"
)

// ErrNotManaged is returned when an operation targets a resource without the managed label.
var ErrNotManaged = errors.New("resource is not managed by Staqio")

// ErrNotFound is returned when a resource does not exist.
var ErrNotFound = errors.New("docker resource not found")

// ErrUnavailable is returned when the Docker Engine cannot be reached.
var ErrUnavailable = errors.New("docker engine unavailable")

// Info describes the connected Docker Engine.
type Info struct {
	APIVersion    string
	ServerVersion string
	OS            string
	Architecture  string
	Containers    int
	Running       int
	NCPU          int
	MemTotal      int64
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

// ContainerDetails is the inspect view used by Staqio.
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

// ContainerSpec is the closed set of parameters Staqio uses to create containers.
// Privileged mode, capability additions, host networking, device access and arbitrary
// binds are intentionally not representable.
type ContainerSpec struct {
	Name          string
	Image         string
	Labels        map[string]string
	Env           []string
	Cmd           []string
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
}

// PullProgress receives human readable image pull progress lines.
type PullProgress func(msg string)

// Engine is the label-scoped Docker abstraction used by Staqio.
type Engine interface {
	// Ping checks connectivity and returns engine information.
	Ping(ctx context.Context) (Info, error)

	// ListContainers lists containers. If managedOnly is true only staqio.managed=true
	// containers are returned; projectID additionally filters by project.
	ListContainers(ctx context.Context, managedOnly bool, projectID string) ([]Container, error)
	// InspectContainer returns details for a managed container by ID or name.
	InspectContainer(ctx context.Context, idOrName string) (ContainerDetails, error)
	// InspectMounts returns the mounts of any container (read-only, used to discover the
	// host paths behind Staqio's own /config and /projects mounts).
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

	// ListNetworks lists networks; managedOnly restricts to Staqio networks.
	ListNetworks(ctx context.Context, managedOnly bool) ([]Network, error)
	// CreateNetwork creates a bridge network with labels.
	CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error)
	// RemoveNetwork removes a managed network.
	RemoveNetwork(ctx context.Context, idOrName string) error
	// ConnectNetwork attaches a container to a managed network (used to attach Staqio's own
	// container so the embedded proxy can reach project web servers).
	ConnectNetwork(ctx context.Context, network, containerID string) error
	// DisconnectNetwork detaches a container from a managed network.
	DisconnectNetwork(ctx context.Context, network, containerID string) error
	// ContainerNetworks lists the network names a container is attached to.
	ContainerNetworks(ctx context.Context, containerID string) ([]string, error)
	// SelfPortBindings returns the host ports published for the given container ports of
	// any container (used to discover how Staqio's own proxy ports are mapped).
	PortBindings(ctx context.Context, containerID string) ([]PortMapping, error)
	// NetworkAccess describes how a container's listeners are reachable from the LAN:
	// through published ports, or directly because it uses host networking or has its
	// own IP on a macvlan/ipvlan network.
	NetworkAccess(ctx context.Context, containerID string) (NetworkAccess, error)

	// ListVolumes lists volumes; managedOnly restricts to Staqio volumes.
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

// IsManaged reports whether a label set carries the managed marker.
func IsManaged(labels map[string]string) bool {
	return labels[LabelManaged] == "true"
}

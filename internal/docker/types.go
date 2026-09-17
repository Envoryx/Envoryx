// Package docker is the only package that talks to the Docker Engine. It exposes a narrow,
// label-scoped Engine interface so that the rest of Staqio can never issue arbitrary
// Docker operations.
package docker

import (
	"context"
	"errors"
	"time"
)

// Labels used to mark and identify Staqio-managed resources.
const (
	LabelManaged     = "staqio.managed"
	LabelProjectID   = "staqio.project.id"
	LabelProjectName = "staqio.project.name"
	LabelService     = "staqio.service"
	LabelVersion     = "staqio.version"
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

// ExecResult is the outcome of a non-interactive command run inside a container.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Stats is a single resource usage sample.
type Stats struct {
	ContainerID string
	CPUPercent  float64
	MemoryBytes int64
	MemoryLimit int64
	SampledAt   time.Time
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

	// ListNetworks lists networks; managedOnly restricts to Staqio networks.
	ListNetworks(ctx context.Context, managedOnly bool) ([]Network, error)
	// CreateNetwork creates a bridge network with labels.
	CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error)
	// RemoveNetwork removes a managed network.
	RemoveNetwork(ctx context.Context, idOrName string) error

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

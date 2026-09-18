// Package hostpath resolves the host-side paths behind Envoryx's own bind mounts.
//
// Envoryx sees project files at /projects inside its container, but the Docker daemon
// resolves bind-mount sources on the host. Project containers therefore need the host
// path (e.g. /mnt/user/development), which is discovered here.
package hostpath

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// ErrUnresolved is returned when no host path is known for a container path.
var ErrUnresolved = errors.New("host path could not be resolved")

// Resolver maps container paths to host paths.
type Resolver struct {
	mu          sync.RWMutex
	overrides   map[string]string // container mount point -> host path
	detected    map[string]string
	selfID      string
	detectErr   error
	inspect     func(ctx context.Context, idOrName string) ([]docker.MountPoint, error)
	selfIDs     func() []string
	inContainer func() bool
	// bareMetal is set when Envoryx runs directly on the Docker host: container paths and
	// host paths are then identical.
	bareMetal   bool
	lastAttempt time.Time
}

// New creates a resolver. overrides maps container directories (e.g. "/projects") to host
// paths configured explicitly by the operator.
func New(engine docker.Engine, overrides map[string]string) *Resolver {
	r := &Resolver{
		overrides:   map[string]string{},
		detected:    map[string]string{},
		selfIDs:     candidateContainerIDs,
		inContainer: runningInContainer,
	}
	if engine != nil {
		r.inspect = engine.InspectMounts
	}
	for k, v := range overrides {
		if v != "" {
			r.overrides[filepath.Clean(k)] = filepath.Clean(v)
		}
	}
	return r
}

// Detect inspects Envoryx's own container and records the host paths of its bind mounts.
// It is safe to call repeatedly; failures are remembered and reported via Status.
func (r *Resolver) Detect(ctx context.Context) error {
	if r.inspect == nil {
		r.mu.Lock()
		r.detectErr = errors.New("docker engine not configured")
		r.mu.Unlock()
		return r.detectErr
	}
	var lastErr error
	for _, id := range r.selfIDs() {
		mounts, err := r.inspect(ctx, id)
		if err != nil {
			lastErr = err
			continue
		}
		r.mu.Lock()
		r.selfID = id
		r.detected = map[string]string{}
		for _, m := range mounts {
			if m.Type == "bind" && m.Destination != "" && m.Source != "" {
				r.detected[filepath.Clean(m.Destination)] = filepath.Clean(m.Source)
			}
		}
		r.detectErr = nil
		r.mu.Unlock()
		return nil
	}
	if !r.inContainer() {
		// Running directly on the host (e.g. `go run` during development): the paths
		// Envoryx sees are the paths the Docker daemon sees.
		r.mu.Lock()
		r.bareMetal = true
		r.detectErr = nil
		r.mu.Unlock()
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("running inside a container but the container id could not be determined")
	}
	r.mu.Lock()
	r.detectErr = lastErr
	r.mu.Unlock()
	return lastErr
}

// EnsureDetected re-runs detection if the last attempt failed (e.g. Docker was not yet
// reachable at startup). Attempts are throttled to one per 10 seconds.
func (r *Resolver) EnsureDetected(ctx context.Context) {
	r.mu.RLock()
	failed := r.detectErr != nil && !r.bareMetal
	last := r.lastAttempt
	r.mu.RUnlock()
	if !failed || time.Since(last) < 10*time.Second {
		return
	}
	r.mu.Lock()
	r.lastAttempt = time.Now()
	r.mu.Unlock()
	_ = r.Detect(ctx)
}

// Resolve returns the host path for an absolute container path.
func (r *Resolver) Resolve(containerPath string) (string, error) {
	containerPath = filepath.Clean(containerPath)
	if !filepath.IsAbs(containerPath) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrUnresolved, containerPath)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := lookup(r.overrides, containerPath); ok {
		return p, nil
	}
	if p, ok := lookup(r.detected, containerPath); ok {
		return p, nil
	}
	if r.bareMetal {
		return containerPath, nil
	}
	if r.detectErr != nil {
		return "", fmt.Errorf("%w for %s: %v", ErrUnresolved, containerPath, r.detectErr)
	}
	return "", fmt.Errorf("%w for %s: no matching bind mount on the Envoryx container", ErrUnresolved, containerPath)
}

// lookup finds the longest mount point that is a prefix of p and rewrites it.
func lookup(m map[string]string, p string) (string, bool) {
	best := ""
	for mp := range m {
		if mp == p || strings.HasPrefix(p, mp+string(filepath.Separator)) || mp == string(filepath.Separator) {
			if len(mp) > len(best) {
				best = mp
			}
		}
	}
	if best == "" {
		return "", false
	}
	rel, err := filepath.Rel(best, p)
	if err != nil {
		return "", false
	}
	return filepath.Join(m[best], rel), true
}

// SelfContainerID returns the id of the Envoryx container ("" on bare metal / unknown).
func (r *Resolver) SelfContainerID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.selfID
}

// Status describes the resolver state for diagnostics.
type Status struct {
	SelfContainerID string            `json:"selfContainerId"`
	Overrides       map[string]string `json:"overrides"`
	Detected        map[string]string `json:"detected"`
	BareMetal       bool              `json:"bareMetal"`
	Error           string            `json:"error,omitempty"`
}

// Status returns the current state.
func (r *Resolver) Status() Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Status{SelfContainerID: r.selfID, Overrides: map[string]string{}, Detected: map[string]string{}, BareMetal: r.bareMetal}
	for k, v := range r.overrides {
		s.Overrides[k] = v
	}
	for k, v := range r.detected {
		s.Detected[k] = v
	}
	if r.detectErr != nil {
		s.Error = r.detectErr.Error()
	}
	return s
}

var containerIDRe = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// runningInContainer reports whether the process runs inside a Docker container.
func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return len(candidateContainerIDs()) > 0
}

func hasDockerEnv() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// candidateContainerIDs returns possible IDs of the container this process runs in.
func candidateContainerIDs() []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// /etc/hostname and /etc/resolv.conf are bind-mounted from /var/lib/docker/containers/<id>/.
	if f, err := os.Open("/proc/self/mountinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := containerIDRe.FindStringSubmatch(sc.Text()); m != nil {
				add(m[1])
			}
		}
		_ = f.Close()
	}
	if f, err := os.Open("/proc/self/cgroup"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := containerIDRe.FindStringSubmatch(sc.Text() + "/"); m != nil {
				add(m[1])
			}
		}
		_ = f.Close()
	}
	// The default container hostname is the short container id; only trust it when the
	// process demonstrably runs in Docker, otherwise a workstation hostname would be tried.
	if h, err := os.Hostname(); err == nil && len(h) >= 12 && hasDockerEnv() {
		add(h)
	}
	return ids
}

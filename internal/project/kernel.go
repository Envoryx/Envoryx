package project

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// kernelTTL is how long the Docker host's kernel release is trusted. It only changes
// with a reboot of the host, so a few minutes of staleness cost nothing.
const kernelTTL = 5 * time.Minute

// kernelCache keeps the Docker host's kernel release: asking Docker takes two calls,
// too many for every project read.
type kernelCache struct {
	mu      sync.Mutex
	release string
	at      time.Time
}

// HostKernel returns the Docker host's kernel release (uname -r), or "" while Docker
// has not told it. Containers run on that kernel, so it decides which service versions
// can start.
func (m *Manager) HostKernel(ctx context.Context) string {
	m.kernel.mu.Lock()
	defer m.kernel.mu.Unlock()
	if !m.kernel.at.IsZero() && time.Since(m.kernel.at) < kernelTTL {
		return m.kernel.release
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// A failed ask keeps the last known release and waits for the TTL, so an unreachable
	// Docker does not add a timeout to every read.
	if info, err := m.engine.Ping(pctx); err == nil {
		m.kernel.release = info.KernelVersion
	}
	m.kernel.at = time.Now()
	return m.kernel.release
}

// noteKernel stores a kernel release from a Ping made for another reason.
func (m *Manager) noteKernel(release string) {
	m.kernel.mu.Lock()
	defer m.kernel.mu.Unlock()
	m.kernel.release, m.kernel.at = release, time.Now()
}

// kernelProblem says why a service Envoryx runs cannot start on the kernel, or "".
func (m *Manager) kernelProblem(svc store.ProjectService, kernel string) string {
	if !svc.Enabled || externalService(&svc) {
		return ""
	}
	return m.catalog.KernelProblem(catalogKey(svc), svc.Version, kernel)
}

// kernelWarnings lists the services of a project that cannot start on the kernel. The
// project stays as it is (it may have been set up on another host or before a kernel
// update); the warning says what to switch to.
func (m *Manager) kernelWarnings(p store.Project, kernel string) []string {
	var out []string
	for _, svc := range p.Services {
		if msg := m.kernelProblem(svc, kernel); msg != "" {
			out = append(out, msg)
		}
	}
	return out
}

// checkKernel refuses newly chosen service versions that cannot start on the kernel.
func (m *Manager) checkKernel(kernel string, services ...store.ProjectService) error {
	for _, svc := range services {
		if msg := m.kernelProblem(svc, kernel); msg != "" {
			return fmt.Errorf("%w: %s", validate.ErrInvalid, msg)
		}
	}
	return nil
}

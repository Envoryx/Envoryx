package project

import (
	"strings"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// ServiceForwards are the containers of a project an SSH client may forward a port to
// (ssh -L 3306:database:3306): their names on the project network, as the application
// reaches them, with the container behind each. That is how a database client or a
// debugger on the developer's machine gets in where the host ports only listen on the
// server itself.
type ServiceForwards struct {
	// Containers maps a network alias (database, redis, node …) to the container name.
	Containers map[string]string
	// Direct says Envoryx shares the project network and dials the containers itself;
	// on bare metal a relay inside the application container does it.
	Direct bool
}

// ForwardTargets lists the containers of a project an SSH client may forward to.
func (m *Manager) ForwardTargets(p store.Project) (ServiceForwards, error) {
	// The plan's switch for the IDE gateway covers every port forwarding over SSH.
	if err := m.Plan().RequireFeature(plan.FeatureIDEGateway); err != nil {
		return ServiceForwards{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return ServiceForwards{}, err
	}
	pl, err := planner.Plan(p)
	if err != nil {
		return ServiceForwards{}, err
	}
	out := ServiceForwards{Containers: map[string]string{}, Direct: planner.paths.SelfContainerID != ""}
	for _, c := range pl.Containers {
		for _, alias := range c.Spec.NetworkAlias {
			out.Containers[strings.ToLower(alias)] = c.Spec.Name
		}
	}
	return out, nil
}

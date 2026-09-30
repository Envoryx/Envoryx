package docker

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestSummaryToContainerListsDualStackBindingOnce(t *testing.T) {
	c := summaryToContainer(container.Summary{Ports: []container.PortSummary{
		{IP: netip.MustParseAddr("0.0.0.0"), PrivatePort: 3000, PublicPort: 28001, Type: "tcp"},
		{IP: netip.MustParseAddr("::"), PrivatePort: 3000, PublicPort: 28001, Type: "tcp"},
		{IP: netip.MustParseAddr("0.0.0.0"), PrivatePort: 9229, PublicPort: 28002, Type: "tcp"},
		{PrivatePort: 80, Type: "tcp"},
	}})
	if len(c.Ports) != 2 || c.Ports[0].HostPort != 28001 || c.Ports[1].HostPort != 28002 {
		t.Fatalf("ports: %+v", c.Ports)
	}
}

func TestPullSummary(t *testing.T) {
	s := pullSummary{layers: map[string]*layerProgress{}}
	if got := s.observe("", "Pulling from envoryx/envoryx-php", 0, 0); got != "Pulling from envoryx/envoryx-php" {
		t.Fatalf("image line: %q", got)
	}
	s.observe("a", "Pulling fs layer", 0, 0)
	s.observe("b", "Pulling fs layer", 0, 0)
	if got := s.observe("a", "Downloading", 10<<20, 100<<20); got != "downloading (10 MB so far, 0 of 2 layers done)" {
		t.Fatalf("unknown totals: %q", got)
	}
	if got := s.observe("b", "Downloading", 40<<20, 100<<20); got != "downloading 25% (50 MB of 200 MB)" {
		t.Fatalf("download: %q", got)
	}
	s.observe("a", "Download complete", 0, 0)
	if got := s.observe("b", "Download complete", 0, 0); got != "extracting (0 of 2 layers done)" {
		t.Fatalf("extract: %q", got)
	}
	if got := s.observe("a", "Pull complete", 0, 0); got != "extracting (1 of 2 layers done)" {
		t.Fatalf("extract 2: %q", got)
	}
	if got := s.observe("b", "Pull complete", 0, 0); got != "" {
		t.Fatalf("done: %q", got)
	}
	if got := s.observe("", "Status: Downloaded newer image for x", 0, 0); got != "Downloaded newer image for x" {
		t.Fatalf("status: %q", got)
	}
}

package instance

import (
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

func TestAdoptableID(t *testing.T) {
	const old, fresh, proj = "9f9cdd0123456789abcdef0123456789", "ed83960123456789abcdef0123456789", "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	project := func(instance string) docker.Container {
		return docker.Container{ID: "c-" + instance, Name: "envoryx-shop-web", Image: "ghcr.io/envoryx/envoryx-php:8.3",
			Labels: map[string]string{docker.LabelManaged: "true", docker.LabelInstance: instance, docker.LabelProjectID: proj}}
	}
	envoryx := func(id, name string) docker.Container {
		return docker.Container{ID: id, Name: name, Image: "ghcr.io/envoryx/envoryx:0.29.1", Labels: map[string]string{"org.opencontainers.image.title": "Envoryx"}}
	}
	self := envoryx("aaaaaaaaaaaa1111", "envoryx")
	projects := map[string]bool{proj: true}

	cases := []struct {
		name       string
		hint       string
		containers []docker.Container
		self       string
		projects   map[string]bool
		want       bool
		reason     string
	}{
		{"same host after a wipe", old, []docker.Container{self, project(old)}, "aaaaaaaaaaaa", projects, true, ""},
		{"bare metal, no Envoryx container", old, []docker.Container{project(old)}, "", projects, true, ""},
		{"backup without the ID", "", []docker.Container{self, project(old)}, "aaaaaaaaaaaa", projects, false, ""},
		{"second machine: nothing of the old instance here", old, []docker.Container{self}, "aaaaaaaaaaaa", projects, false, ""},
		{"containers of unknown projects only", old, []docker.Container{self, project(old)}, "aaaaaaaaaaaa", map[string]bool{}, false, ""},
		{"this instance has containers", old, []docker.Container{self, project(old), project(fresh)}, "aaaaaaaaaaaa", projects, false, "containers of its own"},
		{"the original runs next to it", old, []docker.Container{self, project(old), envoryx("bbbbbbbbbbbb2222", "envoryx-original")}, "aaaaaaaaaaaa", projects, false, "envoryx-original"},
		{"an image without the title label", old, []docker.Container{self, project(old), {ID: "cc", Name: "dev", Image: "localhost:5000/envoryx/envoryx:dev"}}, "aaaaaaaaaaaa", projects, false, "(dev)"},
		{"invalid hint", "../x", []docker.Container{self, project(old)}, "aaaaaaaaaaaa", projects, false, ""},
	}
	for _, c := range cases {
		ok, reason := AdoptableID(c.hint, fresh, c.containers, c.projects, c.self)
		if ok != c.want || (c.reason == "" && reason != "") || !strings.Contains(reason, c.reason) {
			t.Errorf("%s: got %v %q", c.name, ok, reason)
		}
	}
}

func TestBackupRecordsTheInstanceID(t *testing.T) {
	dir := t.TempDir()
	id, _, err := LoadID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := readID(dir); got != id {
		t.Fatalf("readID: %q, want %q", got, id)
	}
	if err := WriteID(dir, "not valid!"); err == nil {
		t.Fatal("an invalid ID must be refused")
	}
}

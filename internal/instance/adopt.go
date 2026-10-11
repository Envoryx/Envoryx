package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
)

// After /config is lost, the next start creates a new instance ID while the project
// containers of the old one keep running on the Docker host. Restoring the instance
// backup brings the projects back but not the ID (see IDFile), so every project would
// meet its own containers as "another instance's" and could not start. The backup
// records the ID it was made under; the first start after the restore adopts it, but
// only when nothing suggests that the instance it belongs to still exists:
//
//   - containers of that ID for the restored projects are on this Docker host (on a
//     second machine there are none, and the new ID is right);
//   - this instance has no containers of its own yet (a second instance that took in a
//     backup of the first keeps its ID);
//   - no other Envoryx container exists on the host, running or stopped (an original
//     still running next to its restored copy keeps its containers; two instances must
//     never share an ID, they would remove each other's resources as orphans).
//
// When the rules say no, the instance keeps its ID and the start logs how to adopt the
// old one by hand.

// AdoptableID reports whether a start after restoring a backup made under hint should
// adopt that ID instead of current. reason explains a refusal ("" when hint names no
// other instance or none of its containers are here).
func AdoptableID(hint, current string, containers []docker.Container, projects map[string]bool, self string) (ok bool, reason string) {
	if hint == "" || hint == current || !validID.MatchString(hint) {
		return false, ""
	}
	theirs, own := 0, 0
	for _, c := range containers {
		if !docker.IsManaged(c.Labels) {
			continue
		}
		switch c.Labels[docker.LabelInstance] {
		case hint:
			if projects[c.ProjectID()] {
				theirs++
			}
		case current:
			own++
		}
	}
	if theirs == 0 {
		return false, ""
	}
	if own > 0 {
		return false, "this instance has containers of its own"
	}
	for _, c := range containers {
		if isSelf(c.ID, self) || docker.IsManaged(c.Labels) || !isEnvoryx(c) {
			continue
		}
		return false, fmt.Sprintf("another Envoryx container (%s) exists on this Docker host and may be that instance", c.Name)
	}
	return true, ""
}

// isSelf matches a container id against this process's container id; either may be
// the short form.
func isSelf(id, self string) bool {
	return self != "" && id != "" && (strings.HasPrefix(id, self) || strings.HasPrefix(self, id))
}

// isEnvoryx recognises an Envoryx container by its image's title label or, for an image
// built without it, by the repository name (ghcr.io/envoryx/envoryx:0.29.1). Project
// containers carry the managed label and are sorted out before.
func isEnvoryx(c docker.Container) bool {
	if c.Labels["org.opencontainers.image.title"] == "Envoryx" {
		return true
	}
	ref, _, _ := strings.Cut(c.Image, "@")
	name := ref[strings.LastIndex(ref, "/")+1:]
	name, _, _ = strings.Cut(name, ":")
	return name == "envoryx"
}

// WriteID replaces the instance ID in the config directory.
func WriteID(configDir, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid instance ID %q", id)
	}
	path := filepath.Join(configDir, IDFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readID returns the instance ID in the config directory, "" when there is none.
func readID(configDir string) string {
	raw, err := os.ReadFile(filepath.Join(configDir, IDFile))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(raw))
	if !validID.MatchString(id) {
		return ""
	}
	return id
}

package instance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IDFile holds the instance ID in the config directory. Envoryx stamps the ID on every
// container, network and volume it creates (docker.LabelInstance), so several instances
// can share a Docker host without taking each other's resources for orphans.
//
// Instance backups leave the file out and a restore keeps the current one, as with the
// secret key: a backup of one instance restored into a second instance on the same host
// must not make the second claim the first one's containers. The ID lives only in this
// file for the same reason; a copy in the database would travel with every backup.
const IDFile = "instance-id"

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,63}$`)

// LoadID returns the instance ID from the config directory and creates it on the first
// start (created is then true). A file whose content is no valid ID is an error rather
// than being replaced: a new ID would turn every resource of the old one into another
// instance's, which Envoryx then leaves alone.
func LoadID(configDir string) (id string, created bool, err error) {
	path := filepath.Join(configDir, IDFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		id = strings.TrimSpace(string(raw))
		if !validID.MatchString(id) {
			return "", false, fmt.Errorf("%s does not hold a valid instance ID (8-64 lowercase letters, digits and dashes); restore the previous value or delete the file to start with a new ID", path)
		}
		return id, false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", false, err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", false, err
	}
	id = hex.EncodeToString(b)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", false, err
	}
	return id, true, nil
}

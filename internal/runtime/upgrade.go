package runtime

import (
	"slices"
	"strconv"
	"strings"
)

// UpgradesInPlace reports whether a database of the flavour key can move from one
// catalogue version to another on its existing data directory: always for a flavour
// whose server upgrades its data on its own (MariaDB, MySQL), otherwise only where the
// target version names the old one in UpgradesFrom. Downgrades are checked elsewhere.
func (c *Catalog) UpgradesInPlace(key, from, to string) bool {
	if from == to {
		return true
	}
	if d, ok := DialectFor(key); ok && d.MajorUpgradeInPlace {
		return true
	}
	target, ok := c.version(key, to)
	return ok && slices.Contains(target.UpgradesFrom, from)
}

// upgradeTarget is a version that takes over the data of from in place and runs on the
// kernel, the default if it is one of them.
func upgradeTarget(r Runtime, from Version, kernel string) (Version, bool) {
	var found *Version
	for i, v := range r.Versions {
		if v.brokenOn(kernel) || !slices.Contains(v.UpgradesFrom, from.Version) {
			continue
		}
		if found == nil || v.Default {
			found = &r.Versions[i]
		}
	}
	if found == nil {
		return Version{}, false
	}
	return *found, true
}

func (c *Catalog) version(key, version string) (Version, bool) {
	r, ok := c.runtimes[key]
	if !ok {
		return Version{}, false
	}
	for _, v := range r.Versions {
		if v.Version == version {
			return v, true
		}
	}
	return Version{}, false
}

// CompareDatabaseVersions orders two catalogue versions of the flavour key by the server
// each one runs, -1, 0 or 1. The version names alone mislead where a version follows a
// moving tag: MariaDB "11" is the image mariadb:11, the newest 11.x (11.8), so it is newer
// than "11.4", while MongoDB "8" is mongo:8.0 and older than "8.2". So the image tags are
// compared, and a tag that is a prefix of the other ("11" of "11.4") is the newer one.
// Versions not in the catalogue are compared by name.
func (c *Catalog) CompareDatabaseVersions(key, a, b string) int {
	ta, tb := a, b
	if v, ok := c.version(key, a); ok {
		ta = imageTag(v.Image)
	}
	if v, ok := c.version(key, b); ok {
		tb = imageTag(v.Image)
	}
	return compareTags(ta, tb)
}

// imageTag returns the version part of an image tag: "mariadb:11.4" -> "11.4",
// "postgres:18-alpine" -> "18".
func imageTag(image string) string {
	tag := image
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		tag = image[i+1:]
	}
	tag, _, _ = strings.Cut(tag, "-")
	return tag
}

// compareTags compares dotted numeric tags; a tag that is a strict prefix of the other
// names the moving line it belongs to and counts as newer.
func compareTags(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(pa) < len(pb):
		return 1
	case len(pa) > len(pb):
		return -1
	}
	return 0
}

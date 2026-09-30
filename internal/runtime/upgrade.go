package runtime

import "slices"

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

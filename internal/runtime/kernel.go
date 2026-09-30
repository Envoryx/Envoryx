package runtime

import (
	"fmt"
	"regexp"
	"strconv"
)

var kernelRe = regexp.MustCompile(`^\s*(\d+)\.(\d+)`)

// ParseKernel reads major and minor from a kernel release as uname -r prints it
// ("6.19.0-31-generic", "6.8.0-1-pve", "5.15.167.4-microsoft-standard-WSL2",
// "6.12.24-Unraid"). ok is false when the string does not start with major.minor.
func ParseKernel(release string) (major, minor int, ok bool) {
	m := kernelRe.FindStringSubmatch(release)
	if m == nil {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// brokenOn reports whether v refuses to start on the kernel release. An unknown or
// unparsable kernel counts as fine: a guess must not lock anyone out.
func (v Version) brokenOn(kernel string) bool {
	if v.BrokenFromKernel == "" {
		return false
	}
	major, minor, ok := ParseKernel(kernel)
	if !ok {
		return false
	}
	fromMajor, fromMinor, ok := ParseKernel(v.BrokenFromKernel)
	if !ok {
		return false
	}
	return major > fromMajor || (major == fromMajor && minor >= fromMinor)
}

// KernelProblem says why a version of a runtime cannot run on a Docker host with the
// given kernel release, or "" when it can, the kernel is unknown or the version is not
// in the catalogue (Resolve reports that).
func (c *Catalog) KernelProblem(key, version, kernel string) string {
	r, ok := c.runtimes[key]
	if !ok {
		return ""
	}
	for _, v := range r.Versions {
		if v.Version == version {
			return kernelProblem(r, v, kernel)
		}
	}
	return ""
}

func kernelProblem(r Runtime, v Version, kernel string) string {
	if !v.brokenOn(kernel) {
		return ""
	}
	msg := fmt.Sprintf("cannot start %s on Linux kernel %s and newer (this host runs %s)", v.Label, v.BrokenFromKernel, kernel)
	if alt, ok := alternative(r, kernel); ok {
		msg += "; switch to " + alt.Label
	}
	return msg
}

// alternative is the version to suggest instead: the default when it runs on the
// kernel, else the first one that does.
func alternative(r Runtime, kernel string) (Version, bool) {
	for _, v := range r.Versions {
		if v.Default && !v.brokenOn(kernel) {
			return v, true
		}
	}
	for _, v := range r.Versions {
		if !v.brokenOn(kernel) {
			return v, true
		}
	}
	return Version{}, false
}

// ForKernel returns the runtimes in display order with the versions that cannot run on
// the kernel release marked unavailable.
func (c *Catalog) ForKernel(kernel string) []Runtime {
	out := c.All()
	for i, r := range out {
		versions := make([]Version, len(r.Versions))
		for j, v := range r.Versions {
			if msg := kernelProblem(r, v, kernel); msg != "" {
				v.Unavailable, v.UnavailableReason = true, msg
			}
			versions[j] = v
		}
		out[i].Versions = versions
	}
	return out
}

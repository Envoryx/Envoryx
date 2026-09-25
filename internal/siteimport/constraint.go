package siteimport

import (
	"regexp"
	"strconv"
	"strings"
)

// A version is major*1e6 + minor*1e3 + patch, so ranges compare as integers.
const (
	vMajor = 1_000_000
	vMinor = 1_000
	vTop   = 999
)

type span struct{ lo, hi int } // inclusive

var (
	versionRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+|\*|x|X))?(?:\.(\d+|\*|x|X))?`)
	orRe      = regexp.MustCompile(`\s*\|\|?\s*`)
	andRe     = regexp.MustCompile(`[\s,]+`)
	// ">= 8.1" is written with a space now and then.
	opSpaceRe = regexp.MustCompile(`([<>=!^~]+)\s+`)
)

// parseVersion reads "8", "8.1", "8.1.3", "8.*"; parts is how many numbers it names.
func parseVersion(s string) (v [3]int, parts int, ok bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return v, 0, false
	}
	for i := 1; i <= 3; i++ {
		if m[i] == "" || m[i] == "*" || m[i] == "x" || m[i] == "X" {
			break
		}
		n, _ := strconv.Atoi(m[i])
		v[i-1] = n
		parts = i
	}
	return v, parts, parts > 0
}

func encode(v [3]int) int { return v[0]*vMajor + v[1]*vMinor + v[2] }

// upper returns the last version a partial version still covers ("8.1" → 8.1.999).
func upper(v [3]int, parts int) int {
	switch parts {
	case 1:
		return v[0]*vMajor + vTop*vMinor + vTop
	case 2:
		return v[0]*vMajor + v[1]*vMinor + vTop
	}
	return encode(v)
}

// termSpan turns one comparison of a Composer constraint into a range; ok is false for
// terms that say nothing about the version (!=, stability flags) or cannot be read.
func termSpan(t string) (span, bool) {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '@'); i >= 0 {
		t = t[:i]
	}
	if t == "" || t == "*" {
		return span{0, 999 * vMajor}, true
	}
	all := span{0, 999 * vMajor}
	for _, op := range []string{">=", "<=", "!=", "==", ">", "<", "^", "~", "="} {
		rest, found := strings.CutPrefix(t, op)
		if !found {
			continue
		}
		v, parts, ok := parseVersion(rest)
		if !ok {
			return span{}, false
		}
		switch op {
		case ">=":
			return span{encode(v), all.hi}, true
		case ">":
			return span{upper(v, parts) + 1, all.hi}, true
		case "<=":
			return span{0, upper(v, parts)}, true
		case "<":
			return span{0, encode(v) - 1}, true
		case "!=":
			return span{}, false
		case "^":
			if v[0] == 0 {
				return span{encode(v), v[1]*vMinor + vTop}, true
			}
			return span{encode(v), (v[0]+1)*vMajor - 1}, true
		case "~":
			if parts <= 2 {
				return span{encode(v), (v[0]+1)*vMajor - 1}, true
			}
			return span{encode(v), v[0]*vMajor + (v[1]+1)*vMinor - 1}, true
		default: // = and ==
			return span{encode(v), upper(v, parts)}, true
		}
	}
	v, parts, ok := parseVersion(t)
	if !ok {
		return span{}, false
	}
	return span{encode(v), upper(v, parts)}, true
}

// matches reports whether some patch release of the minor version (major, minor)
// satisfies the Composer constraint ("^7.4 || ^8.0", ">=8.1 <8.4", "7.4 - 8.2", "8.*").
// An empty or unreadable constraint matches everything.
func matches(constraint string, major, minor int) bool {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return true
	}
	constraint = opSpaceRe.ReplaceAllString(constraint, "$1")
	target := span{major*vMajor + minor*vMinor, major*vMajor + minor*vMinor + vTop}
	for _, alt := range orRe.Split(constraint, -1) {
		s := span{0, 999 * vMajor}
		readable := true
		if a, b, hyphen := strings.Cut(alt, " - "); hyphen {
			va, _, oka := parseVersion(a)
			vb, pb, okb := parseVersion(b)
			if !oka || !okb {
				readable = false
			} else {
				s = span{encode(va), upper(vb, pb)}
			}
		} else {
			for _, t := range andRe.Split(strings.TrimSpace(alt), -1) {
				if t == "" {
					continue
				}
				ts, ok := termSpan(t)
				if !ok {
					continue
				}
				s.lo, s.hi = max(s.lo, ts.lo), min(s.hi, ts.hi)
			}
		}
		if readable && s.lo <= target.hi && target.lo <= s.hi {
			return true
		}
	}
	return false
}

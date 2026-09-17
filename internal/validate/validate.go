// Package validate contains input validation shared by the API and the project manager.
package validate

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalid is the sentinel wrapped by every validation error.
var ErrInvalid = errors.New("invalid input")

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	segmentRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,63}$`)
	envKeyRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	versionRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
)

// Slug checks a DNS-safe identifier used for container and network names.
func Slug(s string) error {
	if !slugRe.MatchString(s) {
		return fmt.Errorf("%w: slug %q must be 1-40 lower-case letters, digits or hyphens and start/end alphanumeric", ErrInvalid, s)
	}
	return nil
}

// Slugify derives a slug from a display name. It may return an empty string.
func Slugify(name string) string {
	var b strings.Builder
	lastHyphen := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '/':
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// ProjectName checks a human readable project name.
func ProjectName(s string) error {
	n := utf8.RuneCountInString(s)
	if n < 2 || n > 64 {
		return fmt.Errorf("%w: project name must be 2-64 characters", ErrInvalid)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: project name contains control characters", ErrInvalid)
		}
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("%w: project name must not start or end with whitespace", ErrInvalid)
	}
	return nil
}

// Username checks a login name.
func Username(s string) error {
	if !usernameRe.MatchString(s) {
		return fmt.Errorf("%w: username must be 2-64 letters, digits, dots, hyphens or underscores", ErrInvalid)
	}
	return nil
}

// RelativePath validates a path relative to a root: no absolute paths, no "..", no empty or
// hidden segments, at most maxDepth segments. It returns the cleaned slash-separated path.
func RelativePath(p string, maxDepth int) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%w: path must not be empty", ErrInvalid)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: path contains NUL", ErrInvalid)
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: path must be relative", ErrInvalid)
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: path must stay inside the projects directory", ErrInvalid)
	}
	segments := strings.Split(cleaned, "/")
	if len(segments) > maxDepth {
		return "", fmt.Errorf("%w: path may have at most %d segments", ErrInvalid, maxDepth)
	}
	for _, seg := range segments {
		if seg == ".." || seg == "." {
			return "", fmt.Errorf("%w: path must not contain '.' or '..' segments", ErrInvalid)
		}
		if !segmentRe.MatchString(seg) {
			return "", fmt.Errorf("%w: path segment %q contains unsupported characters", ErrInvalid, seg)
		}
	}
	return cleaned, nil
}

// OptionalRelativePath is like RelativePath but accepts "" (meaning the root itself).
func OptionalRelativePath(p string, maxDepth int) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", nil
	}
	return RelativePath(p, maxDepth)
}

// ResolveUnder joins rel onto root and verifies (lexically) that the result stays under root.
func ResolveUnder(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(root, abs)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes root", ErrInvalid)
	}
	return abs, nil
}

// EnvKey validates an environment variable name.
func EnvKey(k string) error {
	if !envKeyRe.MatchString(k) {
		return fmt.Errorf("%w: environment variable name %q must match [A-Z_][A-Z0-9_]*", ErrInvalid, k)
	}
	return nil
}

// EnvValue validates an environment variable value.
func EnvValue(v string) error {
	if len(v) > 4096 {
		return fmt.Errorf("%w: environment variable value too long", ErrInvalid)
	}
	for _, r := range v {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("%w: environment variable value contains line breaks or NUL", ErrInvalid)
		}
	}
	return nil
}

// UUID validates a lower-case UUID.
func UUID(s string) error {
	if !uuidRe.MatchString(s) {
		return fmt.Errorf("%w: invalid id", ErrInvalid)
	}
	return nil
}

// Version validates a dotted numeric version key such as "8.4".
func Version(s string) error {
	if !versionRe.MatchString(s) {
		return fmt.Errorf("%w: invalid version %q", ErrInvalid, s)
	}
	return nil
}

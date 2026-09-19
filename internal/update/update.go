// Package update checks GitHub for a newer Envoryx release. One small GET a day against
// the public releases API; nothing about the instance is sent beyond the User-Agent.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the GitHub "latest release" endpoint of the project.
const DefaultURL = "https://api.github.com/repos/envoryx/envoryx/releases/latest"

// Status is what the UI shows.
type Status struct {
	// Current is the running version as built (v0.1.0, main-<sha>, dev).
	Current string `json:"current"`
	// Enabled is false when the check was switched off (ENVORYX_UPDATE_CHECK=false).
	Enabled bool `json:"enabled"`
	// Release reports whether Current is a tagged release; development builds never
	// claim an update is available.
	Release bool `json:"release"`
	// Latest is the newest published release (empty until the first successful check).
	Latest string `json:"latest,omitempty"`
	// Available is true when Latest is newer than Current.
	Available bool `json:"available"`
	// URL links to the release notes.
	URL         string     `json:"url,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CheckedAt   *time.Time `json:"checkedAt,omitempty"`
	// Error is the last check's failure (network down, rate limit); stale data is kept.
	Error string `json:"error,omitempty"`
}

// Checker polls the releases endpoint.
type Checker struct {
	current  string
	url      string
	client   *http.Client
	interval time.Duration
	log      *slog.Logger

	mu     sync.RWMutex
	status Status
}

// New creates a checker for the running version. url "" uses DefaultURL.
func New(current, url string, log *slog.Logger) *Checker {
	if url == "" {
		url = DefaultURL
	}
	return &Checker{
		current:  current,
		url:      url,
		client:   &http.Client{Timeout: 15 * time.Second},
		interval: 24 * time.Hour,
		log:      log,
		status:   Status{Current: current, Enabled: true, Release: parseVersion(current) != nil},
	}
}

// Disabled returns a checker that never runs but still reports the current version.
func Disabled(current string) *Checker {
	c := New(current, "", slog.Default())
	c.status.Enabled = false
	return c
}

// SetInterval overrides the polling interval (tests).
func (c *Checker) SetInterval(d time.Duration) { c.interval = d }

// Status returns the last known state.
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// Run checks shortly after start and then every interval until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	if !c.Status().Enabled {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		if err := c.Check(ctx); err != nil && !errors.Is(err, context.Canceled) {
			c.log.Warn("update check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.interval):
		}
	}
}

type release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// Check fetches the latest release once and updates the status.
func (c *Checker) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Envoryx/"+c.current)
	res, err := c.client.Do(req)
	if err != nil {
		return c.fail(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return c.fail(err)
	}
	if res.StatusCode != http.StatusOK {
		return c.fail(fmt.Errorf("releases endpoint answered %d", res.StatusCode))
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return c.fail(fmt.Errorf("decode release: %w", err))
	}
	if rel.Draft || rel.Prerelease || rel.TagName == "" {
		return c.fail(errors.New("latest release is not a published stable version"))
	}
	now := time.Now().UTC()
	published := rel.PublishedAt.UTC()
	c.mu.Lock()
	c.status.Latest = rel.TagName
	c.status.URL = rel.HTMLURL
	c.status.PublishedAt = &published
	c.status.CheckedAt = &now
	c.status.Error = ""
	c.status.Available = c.status.Release && Newer(rel.TagName, c.current)
	c.mu.Unlock()
	if c.status.Available {
		c.log.Info("newer Envoryx release available", "current", c.current, "latest", rel.TagName)
	}
	return nil
}

func (c *Checker) fail(err error) error {
	now := time.Now().UTC()
	c.mu.Lock()
	c.status.Error = err.Error()
	c.status.CheckedAt = &now
	c.mu.Unlock()
	return err
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// parseVersion returns [major, minor, patch] for a release tag, nil for anything else
// (main-<sha>, dev, dirty builds).
func parseVersion(v string) []int {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil
	}
	out := make([]int, 3)
	for i, s := range m[1:] {
		out[i], _ = strconv.Atoi(s)
	}
	return out
}

// Newer reports whether release a is a higher version than b. Non-release strings are
// never newer and nothing is newer than them.
func Newer(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

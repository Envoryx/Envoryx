package project

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A running container does not mean a working application: PHP may answer every request
// with a 500, the database may be unreachable, a deploy may have left the app in
// maintenance mode. A health check requests a path of the application at an interval –
// the way the proxy does, straight to the web server on the project network, with the
// project's host name – and calls the application down after several failures in a row.
// Down and recovered each send one notification (project.down).
//
// Checks pause while the project is stopped, while an operation (start, deploy, restart …)
// runs on it and while the application container is not running – the reconciler reports
// that case as project.unhealthy already.

// Health states.
const (
	HealthPending = "pending" // no result yet
	HealthUp      = "up"
	HealthFailing = "failing" // failed, but fewer times in a row than the threshold
	HealthDown    = "down"
	HealthPaused  = "paused" // operation running or application container stopped
)

// Limits of a health check.
const (
	minHealthInterval = 10
	maxHealthInterval = 3600
	maxHealthTimeout  = 60
	maxHealthFailures = 20
	maxHealthPath     = 512
)

// HealthStatus is the state of a project's health check for the UI.
type HealthStatus struct {
	State string `json:"state"`
	// Since is when the current state began.
	Since time.Time `json:"since"`
	// Checked is the time of the latest check (zero before the first).
	Checked time.Time `json:"checked,omitzero"`
	// Status is the HTTP status of the latest answer (0 = none).
	Status int `json:"status,omitempty"`
	// LatencyMs is how long the latest answer took.
	LatencyMs int64 `json:"latencyMs,omitempty"`
	// Error says why the latest check failed.
	Error string `json:"error,omitempty"`
	// Failures counts the failed checks in a row.
	Failures int `json:"failures,omitempty"`
	// DownSince is when the application went down (only while down).
	DownSince time.Time `json:"downSince,omitzero"`
}

// HealthResult is the outcome of one check.
type HealthResult struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	// URL is what was requested, as the application sees it.
	URL string `json:"url"`
}

type healthRecord struct {
	HealthStatus
	cfg      store.HealthCheck
	next     time.Time
	inFlight bool
	notified bool // a down notification went out
}

type healthState struct {
	mu        sync.Mutex
	byProject map[string]*healthRecord
	client    *http.Client
}

func (m *Manager) health() *healthState {
	m.healthOnce.Do(func() {
		m.healthSt = &healthState{
			byProject: map[string]*healthRecord{},
			client: &http.Client{
				Transport: &http.Transport{
					Proxy:             nil,
					DialContext:       (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
					DisableKeepAlives: true,
				},
				// A redirect is an answer: /health → /login means the check is wrong or
				// the app is not healthy.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		}
	})
	return m.healthSt
}

// normalizeHealthCheck validates a check; an empty path switches it off.
func normalizeHealthCheck(h store.HealthCheck) (store.HealthCheck, error) {
	h.Path = strings.TrimSpace(h.Path)
	if h.Path == "" {
		return store.HealthCheck{}, nil
	}
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{validate.ErrInvalid}, a...)...)
	}
	if !strings.HasPrefix(h.Path, "/") || strings.HasPrefix(h.Path, "//") || len(h.Path) > maxHealthPath || strings.ContainsAny(h.Path, " \t\r\n#") {
		return h, bad("the health check path must start with / and contain no spaces, like /health")
	}
	if h.Status != 0 && (h.Status < 100 || h.Status > 599) {
		return h, bad("the expected status must be an HTTP status between 100 and 599")
	}
	if h.IntervalSec != 0 && (h.IntervalSec < minHealthInterval || h.IntervalSec > maxHealthInterval) {
		return h, bad("the interval must be %d to %d seconds", minHealthInterval, maxHealthInterval)
	}
	if h.TimeoutSec != 0 && (h.TimeoutSec < 1 || h.TimeoutSec > maxHealthTimeout) {
		return h, bad("the timeout must be 1 to %d seconds", maxHealthTimeout)
	}
	if h.Failures != 0 && (h.Failures < 1 || h.Failures > maxHealthFailures) {
		return h, bad("the failures before an alarm must be 1 to %d", maxHealthFailures)
	}
	if d := h.WithDefaults(); d.TimeoutSec >= d.IntervalSec {
		return h, bad("the timeout must be shorter than the interval")
	}
	// Defaults are stored as zero, so a changed default reaches every project.
	d := store.HealthCheck{Path: h.Path}.WithDefaults()
	if h.Status == d.Status {
		h.Status = 0
	}
	if h.IntervalSec == d.IntervalSec {
		h.IntervalSec = 0
	}
	if h.TimeoutSec == d.TimeoutSec {
		h.TimeoutSec = 0
	}
	if h.Failures == d.Failures {
		h.Failures = 0
	}
	return h, nil
}

// SetHealthCheck stores a project's health check (an empty path switches it off) and
// checks at once.
func (m *Manager) SetHealthCheck(ctx context.Context, id string, h store.HealthCheck) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	h, err := normalizeHealthCheck(h)
	if err != nil {
		return View{}, err
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := m.store.Projects.SetHealthCheck(ctx, id, h); err != nil {
		return View{}, err
	}
	hs := m.health()
	hs.mu.Lock()
	if rec := hs.byProject[id]; rec != nil {
		if rec.notified && !h.Enabled() {
			m.clearDown(id)
		}
		if !h.Enabled() {
			delete(hs.byProject, id)
		} else {
			rec.next = time.Time{}
		}
	}
	hs.mu.Unlock()
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"healthCheck": h}})
	return m.Get(ctx, id)
}

// healthStatus is the state of a project's check (nil without a check or result).
func (m *Manager) healthStatus(p store.Project) *HealthStatus {
	if !p.HealthCheck.Enabled() {
		return nil
	}
	hs := m.health()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	rec := hs.byProject[p.ID]
	if rec == nil {
		return nil
	}
	st := rec.HealthStatus
	return &st
}

// addHealth puts the health check's state and, while down, a warning on a status.
func (m *Manager) addHealth(p store.Project, st *Status) {
	st.Health = m.healthStatus(p)
	if w := healthWarning(p, st.Health); w != "" {
		st.Warnings = append(st.Warnings, w)
	}
}

// healthWarning is the project warning while the application is down.
func healthWarning(p store.Project, st *HealthStatus) string {
	if st == nil || st.State != HealthDown {
		return ""
	}
	return fmt.Sprintf("the health check %s has failed since %s: %s", p.HealthCheck.Path, st.DownSince.Local().Format("15:04"), st.Error)
}

// target is where a project's application answers, and whether it runs.
func (m *Manager) healthTarget(ctx context.Context, p store.Project) (dial string, running bool, err error) {
	paths, err := m.paths()
	if err != nil {
		return "", false, err
	}
	containers, err := m.engine.ListContainers(ctx, false, p.ID)
	if err != nil {
		return "", false, err
	}
	run := map[string]map[string]bool{}
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		if run[c.Service()] == nil {
			run[c.Service()] = map[string]bool{}
		}
		run[c.Service()][c.ProjectID()] = true
	}
	t := m.appTarget(paths.SelfContainerID, p, run)
	return t.Dial, t.Running && t.Dial != "", nil
}

// CheckHealth runs a check once without storing anything – the "Test" button, which
// may try a check that is not saved yet.
func (m *Manager) CheckHealth(ctx context.Context, id string, h store.HealthCheck) (HealthResult, error) {
	if err := validate.UUID(id); err != nil {
		return HealthResult{}, ErrNotFound
	}
	if strings.TrimSpace(h.Path) == "" {
		h.Path = "/"
	}
	h, err := normalizeHealthCheck(h)
	if err != nil {
		return HealthResult{}, err
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return HealthResult{}, err
	}
	dial, running, err := m.healthTarget(ctx, p)
	if err != nil {
		return HealthResult{}, err
	}
	if !running {
		return HealthResult{}, fmt.Errorf("%w: the application container is not running", ErrConflict)
	}
	return m.probe(ctx, p, dial, h.WithDefaults()), nil
}

// probe sends one request.
func (m *Manager) probe(ctx context.Context, p store.Project, dial string, h store.HealthCheck) HealthResult {
	host := DefaultHostname(p.Slug, m.BaseDomain(ctx))
	res := HealthResult{URL: "https://" + host + h.Path}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dial+h.Path, nil)
	if err != nil {
		res.Error = "invalid request: " + err.Error()
		return res
	}
	// What the proxy sends for a visitor on the project URL.
	req.Host = host
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("User-Agent", "Envoryx-HealthCheck/1")
	start := time.Now()
	resp, err := m.health().client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
			res.Error = fmt.Sprintf("no answer within %d s", h.TimeoutSec)
		case strings.Contains(err.Error(), "connection refused"):
			res.Error = "connection refused"
		default:
			res.Error = "request failed"
		}
		return res
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	res.Status = resp.StatusCode
	if resp.StatusCode == h.Status {
		res.OK = true
		return res
	}
	if loc := resp.Header.Get("Location"); loc != "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		res.Error = fmt.Sprintf("HTTP %d instead of %d (redirect to %s)", resp.StatusCode, h.Status, loc)
	} else {
		res.Error = fmt.Sprintf("HTTP %d instead of %d", resp.StatusCode, h.Status)
	}
	return res
}

// RunHealthChecks checks the applications until ctx ends.
func (m *Manager) RunHealthChecks(ctx context.Context, tick time.Duration, log *slog.Logger) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		go m.HealthPass(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// HealthPass starts the checks that are due at now and waits for them.
func (m *Manager) HealthPass(ctx context.Context, now time.Time) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return
	}
	hs := m.health()
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, p := range projects {
		if !p.HealthCheck.Enabled() {
			continue
		}
		seen[p.ID] = true
		cfg := p.HealthCheck.WithDefaults()
		hs.mu.Lock()
		rec := hs.byProject[p.ID]
		if rec == nil || rec.cfg != cfg {
			// New or changed check: start over, but keep a down state until it answers.
			if rec == nil {
				rec = &healthRecord{HealthStatus: HealthStatus{State: HealthPending, Since: now}}
				hs.byProject[p.ID] = rec
			}
			rec.cfg, rec.next, rec.Failures = cfg, time.Time{}, 0
		}
		due := !rec.inFlight && !now.Before(rec.next)
		if due {
			rec.inFlight = true
			rec.next = now.Add(time.Duration(cfg.IntervalSec) * time.Second)
		}
		hs.mu.Unlock()
		if !due {
			continue
		}
		wg.Add(1)
		go func(p store.Project) {
			defer wg.Done()
			m.runHealthCheck(ctx, p, cfg, now)
		}(p)
	}
	// Checks of deleted projects or removed checks go.
	hs.mu.Lock()
	for id, rec := range hs.byProject {
		if !seen[id] && !rec.inFlight {
			if rec.notified {
				m.clearDown(id)
			}
			delete(hs.byProject, id)
		}
	}
	hs.mu.Unlock()
	wg.Wait()
}

func (m *Manager) runHealthCheck(ctx context.Context, p store.Project, cfg store.HealthCheck, now time.Time) {
	hs := m.health()
	pause := func(stopped bool) {
		hs.mu.Lock()
		defer hs.mu.Unlock()
		rec := hs.byProject[p.ID]
		if rec == nil {
			return
		}
		rec.inFlight, rec.Failures = false, 0
		if stopped && rec.State == HealthDown {
			// Stopped on purpose: the outage is over, without a "back" message.
			if rec.notified {
				m.clearDown(p.ID)
			}
			rec.DownSince, rec.notified = time.Time{}, false
			rec.State, rec.Since = HealthPaused, now
		}
		// Otherwise a down application stays down until it answers; the pause does not
		// hide it.
		if rec.State != HealthPaused && rec.State != HealthDown {
			rec.State, rec.Since = HealthPaused, now
		}
	}
	if p.DesiredState != store.DesiredRunning {
		pause(true)
		return
	}
	if m.progress.active(p.ID) != nil {
		pause(false)
		return
	}
	dial, running, err := m.healthTarget(ctx, p)
	if err != nil || !running {
		pause(false)
		return
	}
	res := m.probe(ctx, p, dial, cfg)
	if ctx.Err() != nil {
		pause(false)
		return
	}

	var event *notify.Event
	hs.mu.Lock()
	rec := hs.byProject[p.ID]
	if rec == nil {
		hs.mu.Unlock()
		return
	}
	rec.inFlight = false
	rec.Checked, rec.Status, rec.LatencyMs, rec.Error = now, res.Status, res.LatencyMs, res.Error
	if res.OK {
		if rec.State == HealthDown && rec.notified {
			m.clearDown(p.ID)
			event = &notify.Event{Kind: "project.down", Level: notify.Info, Project: p.Name, Title: p.Name + " is back",
				Message: fmt.Sprintf("GET %s answers %d again, after %s down.", cfg.Path, res.Status, roundDuration(now.Sub(rec.DownSince))),
				Key:     "project.up|" + p.ID}
		}
		if rec.State != HealthUp {
			rec.State, rec.Since = HealthUp, now
		}
		rec.Failures, rec.DownSince, rec.notified = 0, time.Time{}, false
	} else {
		rec.Failures++
		switch {
		case rec.State == HealthDown:
		case rec.Failures >= cfg.Failures:
			rec.State, rec.Since, rec.DownSince, rec.notified = HealthDown, now, now, true
			event = &notify.Event{Kind: "project.down", Level: notify.Error, Project: p.Name, Title: p.Name + " is down",
				Message: fmt.Sprintf("GET %s failed %d times in a row: %s.", cfg.Path, rec.Failures, res.Error),
				Key:     "project.down|" + p.ID}
		case rec.State != HealthFailing:
			rec.State, rec.Since = HealthFailing, now
		}
	}
	hs.mu.Unlock()
	if event != nil {
		if event.Level == notify.Error {
			m.log.Warn("application down", "project", p.Slug, "path", cfg.Path, "err", res.Error)
		} else {
			m.log.Info("application back", "project", p.Slug, "path", cfg.Path)
		}
		m.notify(ctx, *event)
	}
}

// clearDown lets the next down notification of a project through at once.
func (m *Manager) clearDown(id string) {
	if m.notifier != nil {
		m.notifier.Clear("project.down|" + id)
	}
}

// roundDuration is a duration as a person says it: 45 s, 12 min, 3 h 5 min.
func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
}

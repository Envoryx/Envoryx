package project

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Envoryx never swaps the image under a running project: only a restart pulls the tag
// again. So that a rebuilt image (a PHP security fix) doesn't wait for someone to happen
// to restart, the images the projects use are compared with their registry once a day.
// A newer build shows as a warning on the project and in a notification, and, when the
// admin switched it on, the projects are restarted at a set hour.
const (
	// SettingImageAutoRestart restarts the running projects with a newer image daily.
	SettingImageAutoRestart = "image_auto_restart"
	// SettingImageRestartHour is the hour (0-23, the instance's time zone) of that restart.
	SettingImageRestartHour = "image_restart_hour"
	// DefaultImageRestartHour is early enough to be outside working hours anywhere near
	// the instance, and after the runtime images are rebuilt (they start at 04:00 UTC).
	DefaultImageRestartHour = 5

	// settingImageNotified holds the registry digests already notified, per reference,
	// so a restart of Envoryx doesn't announce the same images again.
	settingImageNotified = "image_updates_notified"
	// settingImageRestarted is the restart slot that ran last (RFC 3339).
	settingImageRestarted = "image_restart_last"

	// imageCheckInterval is how often the registries are asked.
	imageCheckInterval = 24 * time.Hour
	// imageCheckDelay lets Envoryx settle after its start before the first check.
	imageCheckDelay = 2 * time.Minute
	// imageRestartWindow is how long after its hour a missed restart slot still runs (an
	// Envoryx update at 05:10). Later, it waits for the next day rather than restarting
	// projects in the middle of the working day.
	imageRestartWindow = time.Hour
)

// ImageUpdateSettings are the admin's choices for newer images.
type ImageUpdateSettings struct {
	AutoRestart bool `json:"autoRestart"`
	Hour        int  `json:"hour"`
}

// ImageCheck is the outcome of the last image check.
type ImageCheck struct {
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Newer are the image references with a newer build in their registry.
	Newer []string `json:"newer"`
	// Error says why some images could not be checked (a socket proxy without
	// DISTRIBUTION, a registry that is down).
	Error string `json:"error,omitempty"`
}

// imageUpdates is the state of the image check.
type imageUpdates struct {
	mu        sync.Mutex
	newer     map[string]string // reference -> registry digest the host doesn't have
	checkedAt time.Time
	err       string
}

// ImageUpdates returns the settings.
func (m *Manager) ImageUpdates(ctx context.Context) ImageUpdateSettings {
	v, err := m.store.Settings.Get(ctx, SettingImageAutoRestart)
	return ImageUpdateSettings{
		AutoRestart: err == nil && v == "true",
		Hour:        m.intSetting(ctx, SettingImageRestartHour, DefaultImageRestartHour, 0, 23),
	}
}

// SetImageUpdates validates and stores the settings.
func (m *Manager) SetImageUpdates(ctx context.Context, s ImageUpdateSettings) error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("%w: hour must be 0-23", validate.ErrInvalid)
	}
	a, err := m.changeSetting(ctx, SettingImageAutoRestart, strconv.FormatBool(s.AutoRestart))
	if err != nil {
		return err
	}
	b, err := m.changeSetting(ctx, SettingImageRestartHour, strconv.Itoa(s.Hour))
	if err != nil {
		return err
	}
	if a || b {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"imageAutoRestart": s.AutoRestart, "imageRestartHour": s.Hour})
	}
	return nil
}

// ImageUpdatesInfo is the settings with the outcome of the last check, for the UI.
type ImageUpdatesInfo struct {
	ImageUpdateSettings
	Check ImageCheck `json:"check"`
}

// ImageUpdatesInfo returns the settings and the last check.
func (m *Manager) ImageUpdatesInfo(ctx context.Context) ImageUpdatesInfo {
	return ImageUpdatesInfo{ImageUpdateSettings: m.ImageUpdates(ctx), Check: m.LastImageCheck()}
}

// LastImageCheck reports the last image check.
func (m *Manager) LastImageCheck() ImageCheck {
	m.images.mu.Lock()
	defer m.images.mu.Unlock()
	out := ImageCheck{Newer: []string{}, Error: m.images.err}
	if !m.images.checkedAt.IsZero() {
		t := m.images.checkedAt
		out.CheckedAt = &t
	}
	for ref := range m.images.newer {
		out.Newer = append(out.Newer, ref)
	}
	sort.Strings(out.Newer)
	return out
}

// newerImages are the references the last check found a newer build for.
func (m *Manager) newerImages() map[string]bool {
	m.images.mu.Lock()
	defer m.images.mu.Unlock()
	out := make(map[string]bool, len(m.images.newer))
	for ref := range m.images.newer {
		out[ref] = true
	}
	return out
}

// pulledNewerImage forgets the newer build of ref the last check found once a pull brought
// it to the host, so the projects stop saying "available" until the next daily check.
func (m *Manager) pulledNewerImage(ctx context.Context, ref string) {
	m.images.mu.Lock()
	digest, ok := m.images.newer[ref]
	m.images.mu.Unlock()
	if !ok {
		return
	}
	info, err := m.engine.InspectImage(ctx, ref)
	if err != nil || !hasDigest(info.RepoDigests, digest) {
		return
	}
	m.images.mu.Lock()
	if m.images.newer[ref] == digest {
		delete(m.images.newer, ref)
	}
	m.images.mu.Unlock()
}

// RunImageUpdates checks the images daily and restarts the projects in the admin's
// slot until ctx ends.
func (m *Manager) RunImageUpdates(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	next := time.Now().Add(imageCheckDelay)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if !now.Before(next) {
				m.CheckImageUpdates(ctx)
				next = now.Add(imageCheckInterval)
			}
			m.restartForImages(ctx, now, log)
		}
	}
}

// CheckImageUpdates asks the registries whether the images the projects use have a newer
// build than the host, and notifies about the ones it hasn't told about yet.
func (m *Manager) CheckImageUpdates(ctx context.Context) ImageCheck {
	users, err := m.imageUsers(ctx)
	if err != nil {
		m.log.Warn("image check: projects unavailable", "err", err)
		return m.LastImageCheck()
	}
	newer := map[string]string{}
	var failed []string
	var cause error
	for ref := range users {
		info, err := m.engine.InspectImage(ctx, ref)
		if err != nil || len(info.RepoDigests) == 0 {
			// Not pulled yet (the next start pulls it), or built on this host.
			continue
		}
		remote, err := m.engine.RemoteDigest(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				return m.LastImageCheck()
			}
			m.log.Debug("image check failed", "image", ref, "err", err)
			failed = append(failed, ref)
			cause = err
			continue
		}
		if !hasDigest(info.RepoDigests, remote) {
			newer[ref] = remote
		}
	}
	m.images.mu.Lock()
	m.images.newer, m.images.checkedAt, m.images.err = newer, time.Now(), ""
	if len(failed) > 0 {
		sort.Strings(failed)
		m.images.err = fmt.Sprintf("%d of %d images could not be checked (%s): %v", len(failed), len(users), strings.Join(failed, ", "), cause)
		m.log.Warn("image check incomplete", "failed", len(failed), "of", len(users), "err", cause)
	}
	m.images.mu.Unlock()
	if len(newer) > 0 {
		m.log.Info("newer images in the registry", "images", len(newer))
	}
	m.notifyNewerImages(ctx, newer, users)
	return m.LastImageCheck()
}

// imageUsers maps the registry images of the projects to the names of the projects that
// use them. Images built from a project Dockerfile have no registry to ask.
func (m *Manager) imageUsers(ctx context.Context) (map[string][]string, error) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		return nil, err
	}
	planner, err := m.planner()
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	for _, p := range projects {
		if p.Lifecycle != store.LifecycleReady {
			continue
		}
		plan, err := planner.Plan(p)
		if err != nil {
			continue
		}
		for _, ref := range plan.Images {
			if isBuildRef(ref) || isRollbackRef(ref) || slices.Contains(users[ref], p.Name) {
				continue
			}
			users[ref] = append(users[ref], p.Name)
		}
	}
	return users, nil
}

// hasDigest reports whether one of an image's repo digests (repo@sha256:...) is digest.
func hasDigest(repoDigests []string, digest string) bool {
	for _, rd := range repoDigests {
		if _, d, ok := strings.Cut(rd, "@"); ok && d == digest {
			return true
		}
	}
	return false
}

// notifyNewerImages sends one notification for the newer builds not announced before. A
// digest is announced once; the next rebuild of the same tag is news again.
func (m *Manager) notifyNewerImages(ctx context.Context, newer map[string]string, users map[string][]string) {
	notified := map[string]string{}
	if v, err := m.store.Settings.Get(ctx, settingImageNotified); err == nil {
		_ = json.Unmarshal([]byte(v), &notified)
	}
	var fresh []string
	for ref, digest := range newer {
		if notified[ref] != digest {
			fresh = append(fresh, ref)
		}
	}
	// Only the images that are still newer are remembered: once the host has one, its
	// next rebuild must be announced again.
	keep := map[string]string{}
	for ref, digest := range newer {
		keep[ref] = digest
	}
	if buf, err := json.Marshal(keep); err == nil && string(buf) != mustJSON(notified) {
		if err := m.store.Settings.Set(ctx, settingImageNotified, string(buf)); err != nil {
			m.log.Warn("image check: remember notified images", "err", err)
		}
	}
	if len(fresh) == 0 {
		return
	}
	sort.Strings(fresh)
	projects := map[string]bool{}
	lines := make([]string, 0, len(fresh))
	for _, ref := range fresh {
		for _, name := range users[ref] {
			projects[name] = true
		}
		lines = append(lines, fmt.Sprintf("%s: %s", ref, strings.Join(users[ref], ", ")))
	}
	msg := strings.Join(lines, "\n") + "\n\n"
	if s := m.ImageUpdates(ctx); s.AutoRestart {
		msg += fmt.Sprintf("The running projects are restarted at %02d:00 to apply them.", s.Hour)
	} else {
		msg += "Restart the projects to apply them."
	}
	m.notify(ctx, notify.Event{
		Kind:    "images.newer",
		Level:   notify.Info,
		Title:   fmt.Sprintf("Newer images for %d %s", len(projects), plural(len(projects), "project", "projects")),
		Message: msg,
		// Every new set of digests is its own event, past the usual cooldown.
		Key: "images.newer|" + strings.Join(fresh, ",") + "|" + newer[fresh[0]],
	})
}

// mustJSON is the JSON of v, "" when it has none.
func mustJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(buf)
}

// restartDue is the latest restart slot at or before now.
func restartDue(hour int, now time.Time) time.Time {
	due := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if due.After(now) {
		due = due.AddDate(0, 0, -1)
	}
	return due
}

// restartForImages restarts, in the admin's slot, the running projects whose containers
// don't run the newest build of their images. It asks the registries first, so a build
// that appeared since the last daily check is part of it.
func (m *Manager) restartForImages(ctx context.Context, now time.Time, log *slog.Logger) {
	s := m.ImageUpdates(ctx)
	if !s.AutoRestart || m.Suspension() != nil {
		return
	}
	due := restartDue(s.Hour, now)
	if now.Sub(due) >= imageRestartWindow {
		return
	}
	if v, err := m.store.Settings.Get(ctx, settingImageRestarted); err == nil {
		if last, err := time.Parse(time.RFC3339, v); err == nil && !last.Before(due) {
			return
		}
	}
	// The slot is taken first, so a restart that fails isn't tried again every minute.
	if err := m.store.Settings.Set(ctx, settingImageRestarted, due.Format(time.RFC3339)); err != nil {
		log.Warn("image restart: record the slot", "err", err)
		return
	}
	m.CheckImageUpdates(ctx)
	ids, err := m.projectsWithImageUpdates(ctx)
	if err != nil {
		log.Warn("image restart: projects unavailable", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	var restarted, failed []string
	for _, p := range ids {
		if _, err := m.Restart(ctx, p.ID); err != nil {
			log.Warn("image restart failed", "project", p.Slug, "err", err)
			failed = append(failed, fmt.Sprintf("%s: %v", p.Name, err))
			continue
		}
		log.Info("project restarted for a newer image", "project", p.Slug)
		restarted = append(restarted, p.Name)
	}
	level, msg := notify.Info, ""
	if len(restarted) > 0 {
		msg = "Restarted with newer images: " + strings.Join(restarted, ", ") + "."
	}
	if len(failed) > 0 {
		level = notify.Warning
		msg = strings.TrimSpace(msg + "\n\nCould not restart:\n" + strings.Join(failed, "\n"))
	}
	m.notify(ctx, notify.Event{
		Kind:    "images.restarted",
		Level:   level,
		Title:   fmt.Sprintf("%d of %d %s restarted for newer images", len(restarted), len(ids), plural(len(ids), "project", "projects")),
		Message: msg,
		Key:     "images.restarted|" + due.Format(time.RFC3339),
	})
}

// projectsWithImageUpdates are the running projects whose containers run an older build
// than the host or the registry has. A project rolled back to its previous image on
// purpose stays where it is.
func (m *Manager) projectsWithImageUpdates(ctx context.Context) ([]store.Project, error) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		return nil, err
	}
	info := m.statusInfo(ctx, projects)
	var out []store.Project
	for _, p := range projects {
		if p.Lifecycle != store.LifecycleReady || p.DesiredState != store.DesiredRunning {
			continue
		}
		st := deriveStatus(p, containers, info)
		if slices.ContainsFunc(st.Services, func(s ServiceStatus) bool { return s.ImageUpdate != "" }) {
			out = append(out, p)
		}
	}
	return out, nil
}

// ProjectsWithNewerImages counts the projects whose containers, at the last reconcile,
// didn't run the newest build of their images.
func (m *Manager) ProjectsWithNewerImages() int {
	n := 0
	for _, st := range m.LastReport().States {
		if slices.ContainsFunc(st.Services, func(s ServiceStatus) bool { return s.ImageUpdate != "" }) {
			n++
		}
	}
	return n
}

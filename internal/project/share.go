package project

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Sharing a project puts it on a temporary public address: a Cloudflare quick tunnel
// (trycloudflare.com – no account, no port forwarding) run by a cloudflared container
// next to the project, pointed at the application the way the proxy reaches it. The
// address is random and changes with every share; the share ends when its time is up,
// when the project stops, or by hand. Anyone who has the address can open the project,
// so it is meant for showing work in progress, not for serving it.
const (
	shareImage = "cloudflare/cloudflared:2026.9.3"
	// shareService labels the tunnel container; it is none of the project's services.
	shareService = "share"
	// labelShareExpires carries the end of the share, so it survives an Envoryx restart.
	labelShareExpires = "envoryx.share.expires"

	shareMinDuration     = 5 * time.Minute
	shareMaxDuration     = 24 * time.Hour
	shareDefaultDuration = time.Hour
	shareURLWait         = 45 * time.Second
)

var shareURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Share is the state of a project's public address.
type Share struct {
	Active bool `json:"active"`
	// State is starting (no address yet), online or stopped (the tunnel ended).
	State     string    `json:"state,omitempty"`
	URL       string    `json:"url,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	// Message is the tunnel's last error when it did not come up.
	Message string `json:"message,omitempty"`
}

// ShareStatus returns the project's share.
func (m *Manager) ShareStatus(ctx context.Context, id string) (Share, error) {
	if err := validate.UUID(id); err != nil {
		return Share{}, ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, id); err != nil {
		return Share{}, err
	}
	c, ok, err := m.shareContainer(ctx, id)
	if err != nil || !ok {
		return Share{}, err
	}
	return m.readShare(ctx, c), nil
}

func (m *Manager) shareContainer(ctx context.Context, id string) (docker.Container, bool, error) {
	containers, err := m.engine.ListContainers(ctx, true, id)
	if err != nil {
		return docker.Container{}, false, err
	}
	for _, c := range containers {
		if c.Service() == shareService {
			return c, true, nil
		}
	}
	return docker.Container{}, false, nil
}

// readShare reads the address from the tunnel's output.
func (m *Manager) readShare(ctx context.Context, c docker.Container) Share {
	s := Share{Active: true, State: "starting", StartedAt: c.Created}
	if t, err := time.Parse(time.RFC3339, c.Labels[labelShareExpires]); err == nil {
		s.ExpiresAt = t
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var lastErr string
	_ = m.engine.StreamLogs(lctx, c.ID, docker.LogOptions{Tail: "200"}, func(l docker.LogLine) {
		if u := shareURLRe.FindString(l.Text); u != "" {
			s.URL = u
		}
		if shareErrorRe.MatchString(l.Text) {
			lastErr = l.Text
		}
	})
	switch {
	case c.State != "running":
		s.State = "stopped"
		s.Message = lastErr
	case s.URL != "":
		s.State = "online"
	default:
		s.Message = lastErr
	}
	return s
}

var shareErrorRe = regexp.MustCompile(` ERR |failed to|error=`)

// StartShare puts the project on a new public address for duration (0 = an hour). A
// share that is running already is replaced: the address changes.
func (m *Manager) StartShare(ctx context.Context, id string, duration time.Duration) (Share, error) {
	if err := validate.UUID(id); err != nil {
		return Share{}, ErrNotFound
	}
	if duration == 0 {
		duration = shareDefaultDuration
	}
	if duration < shareMinDuration || duration > shareMaxDuration {
		return Share{}, fmt.Errorf("%w: a share lasts between %s and %s", validate.ErrInvalid, shareMinDuration, shareMaxDuration)
	}
	unlock, err := m.lock(id)
	if err != nil {
		return Share{}, err
	}
	defer unlock()
	view, err := m.Get(ctx, id)
	if err != nil {
		return Share{}, err
	}
	p := view.Project
	paths, err := m.paths()
	if err != nil {
		return Share{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	target, network := m.shareTarget(paths.SelfContainerID, p)
	if view.Status.State != StateRunning || target == "" {
		return Share{}, fmt.Errorf("%w: start the project first; a share points at the running application", ErrConflict)
	}
	if err := m.removeShare(ctx, id); err != nil {
		return Share{}, err
	}
	if err := m.engine.EnsureImage(ctx, shareImage, m.pullProgress(ctx, p.Slug, shareImage)); err != nil {
		return Share{}, err
	}
	expires := time.Now().Add(duration).UTC().Truncate(time.Second)
	labels := docker.ManagedLabels(p.ID, p.Slug, shareService, paths.EnvoryxVersion)
	labels[labelShareExpires] = expires.Format(time.RFC3339)
	spec := docker.ContainerSpec{
		Name:   ContainerName(p.Slug, store.ServiceKind(shareService)),
		Image:  shareImage,
		Labels: labels,
		Cmd:    []string{"tunnel", "--no-autoupdate", "--url", "http://" + target},
		// The address is gone with the process; a restarted tunnel would get another one.
		RestartPolicy: "no",
		Network:       network,
	}
	cid, err := m.engine.CreateContainer(ctx, spec)
	if err != nil {
		return Share{}, err
	}
	if err := m.engine.StartContainer(ctx, cid); err != nil {
		_ = m.engine.RemoveContainer(context.WithoutCancel(ctx), cid)
		return Share{}, err
	}
	// The address shows in the tunnel's output a few seconds later.
	var share Share
	deadline := time.Now().Add(shareURLWait)
	for {
		c, ok, err := m.shareContainer(ctx, id)
		if err != nil {
			return Share{}, err
		}
		if !ok {
			return Share{}, fmt.Errorf("the tunnel container disappeared")
		}
		share = m.readShare(ctx, c)
		if share.State != "starting" || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return Share{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if share.State == "stopped" {
		_ = m.removeShare(context.WithoutCancel(ctx), id)
		return share, fmt.Errorf("the tunnel did not come up: %s", share.Message)
	}
	m.audit.Log(ctx, audit.ActionProjectShared, "project", id, map[string]any{"name": p.Name, "url": share.URL, "expires": expires})
	return share, nil
}

// shareTarget is the address the tunnel forwards to and the network it joins: inside
// Docker the project network and the application's container, on bare metal the host
// network and the published port.
func (m *Manager) shareTarget(selfID string, p store.Project) (target, network string) {
	target = m.dialFor(selfID, p)
	if cfg, ok := pythonServesApp(p); ok {
		target = m.dialForApp(selfID, p, store.ServicePython, cfg.HostPort, cfg.Port)
	} else if cfg, ok := nodeServesApp(p); ok {
		target = m.dialForDev(selfID, p, cfg)
	}
	if selfID == "" {
		return target, "host"
	}
	return target, NetworkName(p.Slug)
}

// StopShare ends the project's share.
func (m *Manager) StopShare(ctx context.Context, id string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := m.removeShare(ctx, id); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionProjectUnshared, "project", id, map[string]any{"name": p.Name})
	return nil
}

func (m *Manager) removeShare(ctx context.Context, id string) error {
	c, ok, err := m.shareContainer(ctx, id)
	if err != nil || !ok {
		return err
	}
	return m.engine.RemoveContainer(ctx, c.ID)
}

// RunShareJanitor ends shares whose time is up, whose tunnel stopped or whose project no
// longer runs, until ctx ends.
func (m *Manager) RunShareJanitor(ctx context.Context, tick time.Duration, log *slog.Logger) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		m.expireShares(ctx, time.Now(), log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) expireShares(ctx context.Context, now time.Time, log *slog.Logger) {
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		return
	}
	for _, c := range containers {
		if c.Service() != shareService {
			continue
		}
		reason := ""
		expires, perr := time.Parse(time.RFC3339, c.Labels[labelShareExpires])
		switch {
		case perr != nil || !now.Before(expires):
			reason = "expired"
		case c.State != "running":
			reason = "tunnel stopped"
		default:
			if p, err := m.store.Projects.Get(ctx, c.ProjectID()); err != nil || p.DesiredState != store.DesiredRunning {
				reason = "project not running"
			}
		}
		if reason == "" {
			continue
		}
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			log.Warn("share not ended", "container", c.Name, "reason", reason, "err", err)
			continue
		}
		log.Info("share ended", "container", c.Name, "reason", reason)
		m.audit.Log(ctx, audit.ActionProjectUnshared, "project", c.ProjectID(), map[string]any{"reason": reason})
	}
}

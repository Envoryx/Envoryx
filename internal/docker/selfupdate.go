package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Envoryx updates itself by replacing its own container with one of a newer image. The
// container can't do that from inside (stopping it ends the process), so it starts a
// short-lived helper container of the new image, which stops it, creates the new one
// with the same settings and waits for it to become healthy; when it doesn't, the helper
// brings the old container back. These are the only operations on a container Envoryx
// didn't create, and they only ever touch its own.

// SelfImage returns the image reference the container id runs ("ghcr.io/envoryx/envoryx:0.27.1").
func (e *MobyEngine) SelfImage(ctx context.Context, id string) (string, error) {
	c, err := e.inspectRaw(ctx, id)
	if err != nil {
		return "", err
	}
	if c.Config == nil {
		return "", errors.New("the container has no configuration")
	}
	return c.Config.Image, nil
}

// RepoDigest returns the digest the registry gave the local image ref when it was pulled
// (sha256:<hex>, the manifest list's for a multi-platform image), so the update can check
// what it is about to run. An image that was built here and never pulled has none.
func (e *MobyEngine) RepoDigest(ctx context.Context, ref string) (string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", err
	}
	res, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", wrap(err)
	}
	return repoDigest(named.Name(), res.RepoDigests)
}

func repoDigest(name string, repoDigests []string) (string, error) {
	for _, rd := range repoDigests {
		d, err := reference.ParseNormalizedNamed(rd)
		if err != nil {
			continue
		}
		if c, ok := d.(reference.Canonical); ok && d.Name() == name {
			return c.Digest().String(), nil
		}
	}
	return "", fmt.Errorf("the image has no digest from %s: it wasn't pulled from there", name)
}

// ImageForVersion is the image of another version from the same repository as image:
// ghcr.io/envoryx/envoryx:latest and 0.28.0 give ghcr.io/envoryx/envoryx:0.28.0.
func ImageForVersion(image, version string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("the image %q is no reference to update from: %w", image, err)
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !tagRe.MatchString(version) {
		return "", fmt.Errorf("%q is no version", version)
	}
	return reference.FamiliarString(reference.TrimNamed(named)) + ":" + version, nil
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// UpdaterExit is how the helper container ended: its exit code and the end of its output,
// or Err when the wait for it broke off.
type UpdaterExit struct {
	Code   int64
	Output string
	Err    error
}

// updaterOutputTail is how much of the helper's output UpdaterExit keeps: enough for the
// error of a helper that couldn't run.
const updaterOutputTail = 2 << 10

// StartUpdater starts the helper container that replaces the container self with one of
// image: it runs image with cmd, the mounts, environment and networks of self (so it
// reaches the Docker daemon and the configuration like self does) and removes itself
// when done. The channel reports the helper's end. A helper that does its work stops
// self first, so a process that still learns of the end knows the helper gave up, or
// could not even run (an image that doesn't run Envoryx).
func (e *MobyEngine) StartUpdater(ctx context.Context, self, image string, cmd []string) (<-chan UpdaterExit, error) {
	c, err := e.inspectRaw(ctx, self)
	if err != nil {
		return nil, err
	}
	if c.Config == nil || c.HostConfig == nil {
		return nil, errors.New("the container has no configuration")
	}
	name := strings.TrimPrefix(c.Name, "/") + "-updater"
	// No labels: nothing Envoryx cleans up or stops may take the helper for its own.
	// No health check either: the image's checks the server, which the helper doesn't run.
	cfg := &container.Config{Image: image, Cmd: cmd, Env: c.Config.Env, User: c.Config.User,
		Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}
	host := &container.HostConfig{Binds: c.HostConfig.Binds, Mounts: c.HostConfig.Mounts, NetworkMode: c.HostConfig.NetworkMode,
		AutoRemove: true, GroupAdd: c.HostConfig.GroupAdd, SecurityOpt: c.HostConfig.SecurityOpt, ExtraHosts: c.HostConfig.ExtraHosts}
	// A helper left over from an earlier attempt that ended would block the name.
	_, _ = e.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: host})
	if err != nil {
		return nil, wrap(err)
	}
	// The output and the wait are taken before the start, so neither is missed of a helper
	// that ends at once; it removes itself, so the wait is for its removal.
	var out *client.ContainerAttachResult
	if a, err := e.cli.ContainerAttach(ctx, res.ID, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true}); err == nil {
		out = &a
	}
	wait := e.cli.ContainerWait(ctx, res.ID, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	if _, err := e.cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		if out != nil {
			out.Close()
		}
		_, _ = e.cli.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true})
		return nil, wrap(err)
	}
	tail := &tailWriter{max: updaterOutputTail}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		if out != nil {
			_, _ = stdcopy.StdCopy(tail, tail, out.Reader)
		}
	}()
	exited := make(chan UpdaterExit, 1)
	go func() {
		var x UpdaterExit
		select {
		case r := <-wait.Result:
			x.Code = r.StatusCode
			if r.Error != nil && r.Error.Message != "" {
				x.Err = errors.New(r.Error.Message)
			}
		case err := <-wait.Error:
			x.Err = wrap(err)
		case <-ctx.Done():
			x.Err = ctx.Err()
		}
		// The output ends with the container; a stream that hangs on is cut off.
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
		}
		if out != nil {
			out.Close()
		}
		<-copied
		x.Output = tail.String()
		exited <- x
	}()
	return exited, nil
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = slices.Clone(t.buf[over:])
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// ReplaceOptions tune ReplaceContainer.
type ReplaceOptions struct {
	// StopTimeout is how long the old container gets to shut down.
	StopTimeout time.Duration
	// Healthy is how long the new container gets to become healthy.
	Healthy time.Duration
	// BeforeRollback runs after the new container failed and is gone, before the old one
	// starts again (to schedule the restore of a migrated database).
	BeforeRollback func()
	Log            *slog.Logger
}

// ErrRolledBack wraps the reason a new container was given up for the old one.
var ErrRolledBack = errors.New("the new version didn't come up; the old one runs again")

// ReplaceContainer replaces the container id with one of image that has the same name
// and settings. It waits for the new container's health check (or, without one, for it to
// keep running); when the new container fails, it is removed and the old one starts again,
// and the error wraps ErrRolledBack.
func (e *MobyEngine) ReplaceContainer(ctx context.Context, id, image string, opts ReplaceOptions) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	old, err := e.inspectRaw(ctx, id)
	if err != nil {
		return err
	}
	if old.Config == nil || old.HostConfig == nil {
		return errors.New("the container has no configuration")
	}
	name := strings.TrimPrefix(old.Name, "/")
	cfg, err := e.configFor(ctx, old, image)
	if err != nil {
		return err
	}
	netCfg := endpointsFor(old)

	secs := int(opts.StopTimeout.Seconds())
	log.Info("stopping the old container", "container", name)
	if _, err := e.cli.ContainerStop(ctx, old.ID, client.ContainerStopOptions{Timeout: &secs}); err != nil {
		return fmt.Errorf("stopping the old container: %w", wrap(err))
	}
	parked := name + "-previous"
	_, _ = e.cli.ContainerRemove(ctx, parked, client.ContainerRemoveOptions{Force: true})
	if _, err := e.cli.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: parked}); err != nil {
		_, _ = e.cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{})
		return fmt.Errorf("renaming the old container: %w", wrap(err))
	}
	restore := func(cause error) error {
		log.Warn("the new container failed; starting the old one again", "err", cause)
		if opts.BeforeRollback != nil {
			opts.BeforeRollback()
		}
		if _, err := e.cli.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: name}); err != nil {
			log.Warn("renaming the old container back", "err", err)
		}
		if _, err := e.cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("%w: %v; and the old container didn't start again either: %v", ErrRolledBack, cause, err)
		}
		return fmt.Errorf("%w: %v", ErrRolledBack, cause)
	}

	log.Info("creating the new container", "container", name, "image", image)
	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: old.HostConfig, NetworkingConfig: netCfg})
	if err != nil {
		return restore(fmt.Errorf("creating the new container: %w", wrap(err)))
	}
	drop := func() {
		_, _ = e.cli.ContainerRemove(context.WithoutCancel(ctx), res.ID, client.ContainerRemoveOptions{Force: true})
	}
	if _, err := e.cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		drop()
		return restore(fmt.Errorf("starting the new container: %w", wrap(err)))
	}
	if err := e.waitHealthy(ctx, res.ID, opts.Healthy); err != nil {
		drop()
		return restore(err)
	}
	log.Info("the new container is healthy; removing the old one", "container", name)
	if _, err := e.cli.ContainerRemove(ctx, old.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		log.Warn("removing the old container", "err", err)
	}
	return nil
}

// configFor is the old container's configuration with the new image. What the old image
// brought along (its environment, labels, command) is left out, so the new image's own
// apply; what the container's creator set stays.
func (e *MobyEngine) configFor(ctx context.Context, old container.InspectResponse, image string) (*container.Config, error) {
	cfg := *old.Config
	cfg.Image = image
	// Docker sets the host name to the short container id; the new container gets its own.
	if strings.HasPrefix(old.ID, cfg.Hostname) && cfg.Hostname != "" {
		cfg.Hostname = ""
	}
	img, err := e.cli.ImageInspect(ctx, old.Image)
	if err != nil || img.Config == nil {
		// Without the old image the settings can't be told apart; keep them all but the
		// command, which belongs to the image.
		cfg.Cmd, cfg.Entrypoint, cfg.Healthcheck = nil, nil, nil
		return &cfg, nil
	}
	ic := img.Config
	cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(v string) bool { return slices.Contains(ic.Env, v) })
	if cfg.Labels != nil {
		labels := map[string]string{}
		for k, v := range cfg.Labels {
			if iv, ok := ic.Labels[k]; !ok || iv != v {
				labels[k] = v
			}
		}
		cfg.Labels = labels
	}
	if slices.Equal(cfg.Cmd, ic.Cmd) {
		cfg.Cmd = nil
	}
	if slices.Equal(cfg.Entrypoint, ic.Entrypoint) {
		cfg.Entrypoint = nil
	}
	if cfg.WorkingDir == ic.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == ic.User {
		cfg.User = ""
	}
	cfg.Healthcheck = nil
	cfg.ExposedPorts = nil
	cfg.Volumes = nil
	return &cfg, nil
}

// endpointsFor are the old container's networks with the settings its creator chose.
func endpointsFor(old container.InspectResponse) *network.NetworkingConfig {
	if old.NetworkSettings == nil || len(old.NetworkSettings.Networks) == 0 {
		return nil
	}
	out := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	short := old.ID
	if len(short) > 12 {
		short = short[:12]
	}
	for name, ep := range old.NetworkSettings.Networks {
		if ep == nil {
			continue
		}
		aliases := slices.DeleteFunc(slices.Clone(ep.Aliases), func(a string) bool { return a == short })
		out.EndpointsConfig[name] = &network.EndpointSettings{IPAMConfig: ep.IPAMConfig, Links: ep.Links, Aliases: aliases, DriverOpts: ep.DriverOpts}
	}
	return out
}

// waitHealthy waits for a started container's health check to pass, or, for an image
// without one, for it to keep running for 30 seconds.
func (e *MobyEngine) waitHealthy(ctx context.Context, id string, limit time.Duration) error {
	if limit <= 0 {
		limit = 5 * time.Minute
	}
	deadline := time.Now().Add(limit)
	start := time.Now()
	for {
		c, err := e.inspectRaw(ctx, id)
		if err != nil {
			return err
		}
		st := c.State
		switch {
		case st == nil:
		case !st.Running:
			return fmt.Errorf("the new container stopped (exit code %d)%s", st.ExitCode, e.tail(ctx, id))
		case st.Health != nil && st.Health.Status == container.Healthy:
			return nil
		case st.Health != nil && st.Health.Status == container.Unhealthy:
			reason := ""
			if n := len(st.Health.Log); n > 0 && st.Health.Log[n-1] != nil {
				reason = strings.TrimSpace(st.Health.Log[n-1].Output)
			}
			if reason == "" {
				return fmt.Errorf("the new container is unhealthy (its health check failed)")
			}
			return fmt.Errorf("the new container is unhealthy: %s", clip(reason))
		case st.Health == nil && time.Since(start) > 30*time.Second:
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the new container didn't become healthy within %s%s", limit, e.tail(ctx, id))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// tail is the end of a container's log, for an error message.
func (e *MobyEngine) tail(ctx context.Context, id string) string {
	rc, err := e.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "5"})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, io.LimitReader(rc, 64<<10))
	text := strings.Join(strings.Fields(strings.ReplaceAll(out.String(), "\n", " | ")), " ")
	if text == "" {
		return ""
	}
	return ": " + clip(text)
}

// clip shortens a message for the result (it travels to the fleet manager).
func clip(s string) string {
	if len(s) > 500 {
		return s[len(s)-500:]
	}
	return s
}

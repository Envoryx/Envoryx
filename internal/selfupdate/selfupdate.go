// Package selfupdate updates a containerised Envoryx to another version on request of its
// hoster's fleet manager. The running Envoryx pulls the image and starts a helper
// container of it ("envoryx self-update"), which replaces the Envoryx container with one
// of the new image and the same settings. When the new container doesn't become
// healthy, the helper brings the old one back, and a database the new version already
// migrated is restored from the backup taken before the migration. ResultFile in the
// configuration directory tells the Envoryx that runs afterwards how it went.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/instance"
)

// ResultFile records the last update in the configuration directory.
const ResultFile = "update.json"

// Result is the last update.
type Result struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Image     string    `json:"image"`
	StartedAt time.Time `json:"startedAt"`
	// FinishedAt is zero while the update runs.
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	OK         bool      `json:"ok"`
	// RolledBack is set when the old version runs again.
	RolledBack bool   `json:"rolledBack,omitempty"`
	Error      string `json:"error,omitempty"`
	// Reported is set once the Envoryx that runs afterwards logged the result.
	Reported bool `json:"reported,omitempty"`
}

// ReadResult returns the last update; nil when there was none.
func ReadResult(configDir string) (*Result, error) {
	b, err := os.ReadFile(filepath.Join(configDir, ResultFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", ResultFile, err)
	}
	return &r, nil
}

func writeResult(configDir string, r Result) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := filepath.Join(configDir, "."+ResultFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(configDir, ResultFile))
}

// TakeUnreported returns a finished update the running Envoryx hasn't logged yet and marks
// it as logged.
func TakeUnreported(configDir string) (*Result, error) {
	r, err := ReadResult(configDir)
	if err != nil || r == nil || r.Reported || r.FinishedAt.IsZero() {
		return nil, err
	}
	r.Reported = true
	return r, writeResult(configDir, *r)
}

// Engine is what the update needs from Docker.
type Engine interface {
	SelfImage(ctx context.Context, id string) (string, error)
	ImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, progress docker.PullProgress) error
	StartUpdater(ctx context.Context, self, image string, cmd []string) (<-chan docker.UpdaterExit, error)
}

// Updater starts updates of the running Envoryx.
type Updater struct {
	Engine Engine
	// SelfID returns the id of the Envoryx container ("" outside a container).
	SelfID    func() string
	ConfigDir string
	Version   string
	// BeforeStop runs before the helper starts: the container is about to be stopped
	// for the update (a restart, not a shutdown of the projects).
	BeforeStop func()
	// Failed runs when an update ended while this process still runs: it didn't start,
	// or the helper gave up without replacing the container. The result is recorded by
	// then (unreported, when there is one to log).
	Failed func()
	Log    *slog.Logger

	mu      sync.Mutex
	running bool
	lastErr string
}

// Status is what the fleet manager learns about updates.
type Status struct {
	// Available is set when Envoryx can update itself; Reason says why not.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Image     string `json:"image,omitempty"`
	// Running is set while an update is prepared (the image pulled) or carried out.
	Running   bool    `json:"running,omitempty"`
	LastError string  `json:"lastError,omitempty"`
	Last      *Result `json:"last,omitempty"`
}

// Status reports whether the instance can update itself and how the last update went.
func (u *Updater) Status(ctx context.Context) Status {
	var s Status
	s.Image, s.Reason = u.image(ctx)
	s.Available = s.Reason == ""
	u.mu.Lock()
	s.Running, s.LastError = u.running, u.lastErr
	u.mu.Unlock()
	s.Last, _ = ReadResult(u.ConfigDir)
	if s.Last != nil && s.Last.FinishedAt.IsZero() && !s.Running {
		// Started by a process that is gone: the helper is at work, or died.
		s.Running = time.Since(s.Last.StartedAt) < 15*time.Minute
	}
	return s
}

func (u *Updater) image(ctx context.Context) (image, reason string) {
	if u == nil || u.Engine == nil {
		return "", "Envoryx doesn't reach Docker"
	}
	self := ""
	if u.SelfID != nil {
		self = u.SelfID()
	}
	if self == "" {
		return "", "Envoryx doesn't run in a container it knows"
	}
	img, err := u.Engine.SelfImage(ctx, self)
	if err != nil {
		return "", "the own container can't be inspected: " + err.Error()
	}
	return img, ""
}

// Start pulls the image of version and starts the helper that replaces the container; it
// returns once the pull began. The result shows in Status.
func (u *Updater) Start(ctx context.Context, version string) error {
	image, reason := u.image(ctx)
	if reason != "" {
		return errors.New(reason)
	}
	target, err := docker.ImageForVersion(image, version)
	if err != nil {
		return err
	}
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return errors.New("an update is under way already")
	}
	u.running, u.lastErr = true, ""
	u.mu.Unlock()
	to := strings.TrimPrefix(strings.TrimSpace(version), "v")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := u.run(ctx, target, to)
		u.mu.Lock()
		u.running = false
		if err != nil {
			u.lastErr = err.Error()
		}
		u.mu.Unlock()
		if err != nil {
			u.Log.Warn("update: not started", "image", target, "err", err)
		}
		// A successful update ends this process before run returns: whatever returns
		// here, the update didn't happen, and the fleet manager should hear it now.
		if u.Failed != nil {
			u.Failed()
		}
	}()
	return nil
}

func (u *Updater) run(ctx context.Context, target, to string) error {
	u.Log.Info("update: pulling the image", "image", target)
	if err := u.Engine.PullImage(ctx, target, nil); err != nil {
		// An image that is there already (built locally, an air-gapped host) will do.
		if ok, _ := u.Engine.ImageExists(ctx, target); !ok {
			return fmt.Errorf("pulling %s: %w", target, err)
		}
		u.Log.Warn("update: pull failed, using the local image", "image", target, "err", err)
	}
	r := Result{From: strings.TrimPrefix(u.Version, "v"), To: to, Image: target, StartedAt: time.Now().UTC()}
	if err := writeResult(u.ConfigDir, r); err != nil {
		return err
	}
	if u.BeforeStop != nil {
		u.BeforeStop()
	}
	self := u.SelfID()
	cmd := []string{"self-update", "--container", self, "--image", target}
	u.Log.Info("update: handing over to the helper container", "image", target)
	exited, err := u.Engine.StartUpdater(ctx, self, target, cmd)
	if err != nil {
		r.FinishedAt, r.Error = time.Now().UTC(), "the helper container didn't start: "+err.Error()
		r.Reported = true
		_ = writeResult(u.ConfigDir, r)
		return errors.New(r.Error)
	}
	// The helper stops this container before it ends, so hearing of its end means it gave
	// up first: the new image can't run it, or it failed before the swap.
	var x docker.UpdaterExit
	select {
	case x = <-exited:
	case <-ctx.Done():
		x.Err = ctx.Err()
	}
	if x.Err != nil {
		// No verdict: the helper may still be at work. Status treats the update as running
		// for a while and the result file tells the rest.
		u.Log.Warn("update: lost sight of the helper container", "err", x.Err)
		return nil
	}
	if last, err := ReadResult(u.ConfigDir); err == nil && last != nil && last.Image == r.Image && last.StartedAt.Equal(r.StartedAt) && !last.FinishedAt.IsZero() {
		// The helper recorded why it gave up.
		u.Log.Warn("update: the helper container gave up", "image", target, "err", last.Error)
		return nil
	}
	r.FinishedAt = time.Now().UTC()
	r.Error = fmt.Sprintf("the helper container of %s ended without replacing Envoryx (exit code %d)", target, x.Code)
	if x.Output != "" {
		r.Error += ": " + lastLines(x.Output, 3)
	}
	u.Log.Warn("update: failed, Envoryx keeps running the old version", "image", target, "err", r.Error)
	return writeResult(u.ConfigDir, r)
}

// lastLines returns the last n lines of s, joined by " / ".
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, " / ")
}

// Replacer is what the helper needs from Docker.
type Replacer interface {
	ReplaceContainer(ctx context.Context, id, image string, opts docker.ReplaceOptions) error
}

// Helper is the helper container's work: replace the container, bring the old one back
// when the new one fails, record the result.
type Helper struct {
	Engine    Replacer
	ConfigDir string
	// Backups are the instance backups; a pre-migration backup taken by the new version
	// is restored when the old one comes back.
	Backups *instance.Store
	Log     *slog.Logger
}

// Run replaces container with one of image.
func (h *Helper) Run(ctx context.Context, container, image string) error {
	r, err := ReadResult(h.ConfigDir)
	if err != nil || r == nil || r.Image != image {
		r = &Result{Image: image, StartedAt: time.Now().UTC()}
	}
	err = h.Engine.ReplaceContainer(ctx, container, image, docker.ReplaceOptions{
		StopTimeout: 2 * time.Minute,
		Healthy:     5 * time.Minute,
		Log:         h.Log,
		BeforeRollback: func() {
			h.restoreMigrated(r.StartedAt)
		},
	})
	r.FinishedAt = time.Now().UTC()
	r.OK = err == nil
	r.RolledBack = errors.Is(err, docker.ErrRolledBack)
	if err != nil {
		r.Error = err.Error()
	}
	if werr := writeResult(h.ConfigDir, *r); werr != nil {
		h.Log.Warn("update: recording the result", "err", werr)
	}
	return err
}

// restoreMigrated schedules the restore of the backup the new version took before it
// migrated the database, so the old version finds a database it can open.
func (h *Helper) restoreMigrated(since time.Time) {
	if h.Backups == nil {
		return
	}
	list, err := h.Backups.List()
	if err != nil {
		h.Log.Warn("update: listing instance backups", "err", err)
		return
	}
	for _, b := range list {
		if b.Kind == instance.KindPreMigrate && !b.CreatedAt.Before(since) {
			if err := h.Backups.ScheduleRestore(b.ID, "update rollback", ""); err != nil {
				h.Log.Warn("update: scheduling the restore of the database", "backup", b.ID, "err", err)
				return
			}
			h.Log.Info("update: the old version restores the database from before the migration", "backup", b.ID)
			return
		}
	}
}

package selfupdate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

type fakeEngine struct {
	mu       sync.Mutex
	image    string
	pullErr  error
	local    bool
	started  []string
	startErr error
	// exit is how the helper ends; nil while it is at work (it would stop this process).
	exit *docker.UpdaterExit
}

func (f *fakeEngine) SelfImage(context.Context, string) (string, error) { return f.image, nil }
func (f *fakeEngine) ImageExists(context.Context, string) (bool, error) { return f.local, nil }
func (f *fakeEngine) PullImage(context.Context, string, docker.PullProgress) error {
	return f.pullErr
}
func (f *fakeEngine) StartUpdater(_ context.Context, _, image string, cmd []string) (<-chan docker.UpdaterExit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append([]string{image}, cmd...)
	ch := make(chan docker.UpdaterExit, 1)
	if f.exit != nil {
		ch <- *f.exit
	}
	return ch, f.startErr
}

func (f *fakeEngine) helper() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func newUpdater(t *testing.T, f *fakeEngine, self string) (*Updater, *bool) {
	stopped := false
	return &Updater{Engine: f, SelfID: func() string { return self }, ConfigDir: t.TempDir(), Version: "v0.27.1",
		BeforeStop: func() { stopped = true }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, &stopped
}

func waitDone(t *testing.T, u *Updater) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		u.mu.Lock()
		running := u.running
		u.mu.Unlock()
		if !running {
			return u.Status(context.Background())
		}
		if time.Now().After(deadline) {
			t.Fatal("the update didn't get going")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdaterHandsOverToTheHelper(t *testing.T) {
	f := &fakeEngine{image: "ghcr.io/envoryx/envoryx:latest"}
	u, stopped := newUpdater(t, f, "abc123")
	if s := u.Status(context.Background()); !s.Available || s.Image != f.image {
		t.Fatalf("status: %+v", s)
	}
	if err := u.Start(context.Background(), "v0.28.0"); err != nil {
		t.Fatal(err)
	}
	// The helper at work stops this process: the update stays running until then.
	deadline := time.Now().Add(5 * time.Second)
	for f.helper() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	want := []string{"ghcr.io/envoryx/envoryx:0.28.0", "self-update", "--container", "abc123", "--image", "ghcr.io/envoryx/envoryx:0.28.0"}
	if got := f.helper(); !slices.Equal(got, want) || !*stopped {
		t.Fatalf("helper %v, before stop %v", got, *stopped)
	}
	r, _ := ReadResult(u.ConfigDir)
	if r == nil || r.From != "0.27.1" || r.To != "0.28.0" || !r.FinishedAt.IsZero() {
		t.Fatalf("result: %+v", r)
	}
	if s := u.Status(context.Background()); !s.Running {
		t.Error("an update the helper carries out shows as running")
	}
}

func TestUpdaterRefuses(t *testing.T) {
	u, _ := newUpdater(t, &fakeEngine{image: "envoryx:dev"}, "")
	if s := u.Status(context.Background()); s.Available || s.Reason == "" {
		t.Fatalf("outside a container: %+v", s)
	}
	if err := u.Start(context.Background(), "0.28.0"); err == nil {
		t.Fatal("started outside a container")
	}

	f := &fakeEngine{image: "ghcr.io/envoryx/envoryx:latest", pullErr: errors.New("manifest unknown")}
	u, stopped := newUpdater(t, f, "abc123")
	// Every update that ends while the process runs failed and is passed on at once.
	var failed atomic.Int32
	u.Failed = func() { failed.Add(1) }
	waitFailed := func(n int32) {
		for i := 0; failed.Load() < n && i < 500; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if failed.Load() != n {
			t.Fatalf("failed %d times, want %d", failed.Load(), n)
		}
	}
	if err := u.Start(context.Background(), "9.9.9"); err != nil {
		t.Fatal(err)
	}
	if s := waitDone(t, u); s.LastError == "" || f.helper() != nil || *stopped {
		t.Fatalf("a failed pull: %+v", s)
	}
	waitFailed(1)
	// An image that is there already does without the pull. This one can't run the
	// helper: it ends at once, and the update is recorded as failed.
	f.local = true
	f.exit = &docker.UpdaterExit{Code: 3, Output: "starting\nboom"}
	if err := u.Start(context.Background(), "9.9.9"); err != nil {
		t.Fatal(err)
	}
	s := waitDone(t, u)
	waitFailed(2)
	if s.LastError != "" || f.helper() == nil || s.Running {
		t.Fatalf("a local image: %+v", s)
	}
	if r, _ := TakeUnreported(u.ConfigDir); r == nil || r.OK || r.FinishedAt.IsZero() || r.From != "0.27.1" || !strings.Contains(r.Error, "exit code 3") || !strings.Contains(r.Error, "starting / boom") {
		t.Fatalf("a helper that couldn't run: %+v", r)
	}
	if err := u.Start(context.Background(), "not a version!"); err == nil {
		t.Error("a bad version passed")
	}
}

type fakeReplacer struct{ err error }

func (f fakeReplacer) ReplaceContainer(_ context.Context, _, _ string, opts docker.ReplaceOptions) error {
	if f.err != nil && opts.BeforeRollback != nil {
		opts.BeforeRollback()
	}
	return f.err
}

func TestHelperRecordsTheResult(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := writeResult(dir, Result{From: "0.27.1", To: "0.28.0", Image: "img:0.28.0", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := &Helper{Engine: fakeReplacer{}, ConfigDir: dir, Log: log}
	if err := h.Run(context.Background(), "abc", "img:0.28.0"); err != nil {
		t.Fatal(err)
	}
	r, err := TakeUnreported(dir)
	if err != nil || r == nil || !r.OK || r.From != "0.27.1" {
		t.Fatalf("result: %+v %v", r, err)
	}
	if again, _ := TakeUnreported(dir); again != nil {
		t.Fatal("reported twice")
	}

	h.Engine = fakeReplacer{err: errors.Join(docker.ErrRolledBack, errors.New("unhealthy"))}
	if err := h.Run(context.Background(), "abc", "img:0.29.0"); err == nil {
		t.Fatal("no error after a rollback")
	}
	if r, _ := TakeUnreported(dir); r == nil || r.OK || !r.RolledBack || r.Error == "" {
		t.Fatalf("rolled back: %+v", r)
	}
}

func TestImageForVersion(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/envoryx/envoryx:latest":                "ghcr.io/envoryx/envoryx:0.28.0",
		"ghcr.io/envoryx/envoryx@sha256:" + sha("a"):    "ghcr.io/envoryx/envoryx:0.28.0",
		"envoryx/envoryx":                               "envoryx/envoryx:0.28.0",
		"registry.example.net:5000/team/envoryx:0.27.1": "registry.example.net:5000/team/envoryx:0.28.0",
	} {
		if got, err := docker.ImageForVersion(in, "v0.28.0"); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
}

func sha(c string) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c[0]
	}
	return string(b)
}

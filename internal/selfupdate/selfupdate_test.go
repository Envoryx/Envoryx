package selfupdate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
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
}

func (f *fakeEngine) SelfImage(context.Context, string) (string, error) { return f.image, nil }
func (f *fakeEngine) ImageExists(context.Context, string) (bool, error) { return f.local, nil }
func (f *fakeEngine) PullImage(context.Context, string, docker.PullProgress) error {
	return f.pullErr
}
func (f *fakeEngine) StartUpdater(_ context.Context, _, image string, cmd []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append([]string{image}, cmd...)
	return f.startErr
}

func (f *fakeEngine) helper() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func newUpdater(t *testing.T, f *fakeEngine, self string) (*Updater, *bool) {
	stopped := false
	return &Updater{Engine: f, SelfID: func() string { return self }, ConfigDir: t.TempDir(), Version: "0.27.1",
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
	waitDone(t, u)
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
	if err := u.Start(context.Background(), "9.9.9"); err != nil {
		t.Fatal(err)
	}
	if s := waitDone(t, u); s.LastError == "" || f.helper() != nil || *stopped {
		t.Fatalf("a failed pull: %+v", s)
	}
	// An image that is there already does without the pull.
	f.local = true
	if err := u.Start(context.Background(), "9.9.9"); err != nil {
		t.Fatal(err)
	}
	if s := waitDone(t, u); s.LastError != "" || f.helper() == nil {
		t.Fatalf("a local image: %+v", s)
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

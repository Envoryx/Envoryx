package dockertest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// follow starts a following StreamLogs on a fresh running container and returns what it
// emitted so far plus a channel that closes when the stream ends.
func follow(t *testing.T, f *Fake, name string) (string, func() []string, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.AddImage("x")
	id, err := f.CreateContainer(ctx, docker.ContainerSpec{Name: name, Image: "x", Labels: docker.ManagedLabels("p", "shop", "php", "test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	var (
		mu  sync.Mutex
		got []string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.StreamLogs(ctx, id, docker.LogOptions{Follow: true}, func(l docker.LogLine) {
			mu.Lock()
			got = append(got, l.Text)
			mu.Unlock()
		})
	}()
	return id, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}, done
}

func waitLen(t *testing.T, got func() []string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(got()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("got %q, want %d lines", got(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFollowPicksUpReplacedOutput(t *testing.T) {
	f := New()
	f.Logs["envoryx-shop-php"] = []docker.LogLine{{Text: "one"}, {Text: "two"}}
	_, got, _ := follow(t, f, "envoryx-shop-php")
	waitLen(t, got, 2)

	// A recreated container starts over with less output than its predecessor had.
	f.SetLogs("envoryx-shop-php", docker.LogLine{Text: "fresh"})
	waitLen(t, got, 3)
	if g := got(); g[2] != "fresh" {
		t.Fatalf("got %q", g)
	}
}

func TestFollowOfARemovedContainerSkipsItsSuccessor(t *testing.T) {
	f := New()
	f.Logs["envoryx-shop-php"] = []docker.LogLine{{Text: "one"}, {Text: "two"}}
	id, got, done := follow(t, f, "envoryx-shop-php")
	waitLen(t, got, 2)

	if err := f.RemoveContainer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.SetLogs("envoryx-shop-php", docker.LogLine{Text: "successor"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream of a removed container did not end")
	}
	if g := got(); len(g) != 2 {
		t.Fatalf("got %q", g)
	}
}

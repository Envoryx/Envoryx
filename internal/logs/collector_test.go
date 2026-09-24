package logs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCollectorFollowsCatchesUpAndSurvivesRecreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := dockertest.New()
	eng.AddImage("x")
	store, _ := OpenStore(t.TempDir())
	c := &Collector{
		Engine:        eng,
		Store:         store,
		Collect:       func(ctr docker.Container) bool { return ctr.ProjectID() != "" && ctr.Service() != "git" },
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		FlushInterval: 10 * time.Millisecond,
	}
	create := func(service string) string {
		t.Helper()
		id, err := eng.CreateContainer(ctx, docker.ContainerSpec{Name: "envoryx-shop-" + service, Image: "x", Labels: docker.ManagedLabels(testProject, "shop", service, "test")})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	k := Key{Project: testProject, Service: "php"}
	stored := func() string { return texts(scanAll(t, store, k, time.Time{}, time.Time{})) }
	base := time.Now().Add(-time.Hour).UTC()
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) }

	eng.Logs["envoryx-shop-php"] = []docker.LogLine{{Time: at(0), Text: "boot"}}
	eng.Logs["envoryx-shop-git"] = []docker.LogLine{{Time: at(0), Text: "clone"}}
	php := create("php")
	create("git")

	c.Pass(ctx, true)
	waitFor(t, "the existing output", func() bool { return stored() == "boot" })
	eng.AppendLogs("envoryx-shop-php", docker.LogLine{Time: at(1), Text: "request"})
	waitFor(t, "a followed line", func() bool { return stored() == "boot,request" })
	c.Pass(ctx, true) // already followed: no second reader
	if c.Following() != 1 {
		t.Fatalf("following %d containers", c.Following())
	}

	// The container is thrown away and created again from the plan: Docker's log
	// starts over, the old lines come again and must not be stored twice.
	if err := eng.RemoveContainer(ctx, php); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the follower to end", func() bool { return c.Following() == 0 })
	eng.SetLogs("envoryx-shop-php", docker.LogLine{Time: at(0), Text: "boot"}, docker.LogLine{Time: at(1), Text: "request"}, docker.LogLine{Time: at(2), Text: "after recreate"})
	php = create("php")
	c.Pass(ctx, true)
	waitFor(t, "the recreated container", func() bool { return stored() == "boot,request,after recreate" })

	// Output of a container that stopped while nobody followed it is caught up once.
	c.Pass(ctx, false)
	if c.Following() != 0 {
		t.Fatal("disabled: nothing may be followed")
	}
	eng.AppendLogs("envoryx-shop-php", docker.LogLine{Time: at(3), Text: "shutdown"})
	eng.SetState("envoryx-shop-php", "exited")
	c.Pass(ctx, true)
	waitFor(t, "the stopped container's last line", func() bool { return stored() == "boot,request,after recreate,shutdown" })
	waitFor(t, "the catch-up to end", func() bool { return c.Following() == 0 })
	c.Pass(ctx, true)
	if c.Following() != 0 {
		t.Fatal("a stopped container is read only once")
	}
	if store.Has(Key{Project: testProject, Service: "git"}) {
		t.Fatal("Collect decides which services are kept")
	}

	// Nothing older than the floor is collected.
	c.Floor = func() time.Time { return at(10) }
	eng.SetLogs("envoryx-shop-web", docker.LogLine{Time: at(5), Text: "old"}, docker.LogLine{Time: at(11), Text: "new"})
	create("web")
	c.Pass(ctx, true)
	web := Key{Project: testProject, Service: "web"}
	waitFor(t, "the web container", func() bool { return texts(scanAll(t, store, web, time.Time{}, time.Time{})) == "new" })
	c.StopAll()
}

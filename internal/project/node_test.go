package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

func TestNodeServiceLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Front", true)
	req.Node = &NodeRequest{Version: "22"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	node, ok := e.engine.Container("staqio-front-node")
	if !ok || node.State != "running" || node.Spec.Image != "ghcr.io/seramos/staqio-node:22" {
		t.Fatalf("node container: %+v", node)
	}
	if node.Spec.User != "1000:1000" || node.Spec.WorkingDir != "/var/www/html" || strings.Join(node.Spec.Cmd, " ") != "sleep infinity" {
		t.Fatalf("node spec: %+v", node.Spec)
	}
	if node.Spec.Mounts[0].Source != "/host/development/front" || node.Spec.Mounts[0].ReadOnly {
		t.Fatalf("node must mount the project rw: %+v", node.Spec.Mounts)
	}
	env := strings.Join(node.Spec.Env, "\n")
	if !strings.Contains(env, "APP_ENV=local") || !strings.Contains(env, "HOME=/tmp") {
		t.Fatalf("node env: %v", node.Spec.Env)
	}
	calls := strings.Join(e.engine.Calls, " ")
	if strings.Index(calls, "start:staqio-front-php") > strings.Index(calls, "start:staqio-front-node") || strings.Index(calls, "start:staqio-front-node") > strings.Index(calls, "start:staqio-front-web") {
		t.Fatalf("start order php → node → web expected: %s", calls)
	}
	if len(view.Status.Services) != 3 {
		t.Fatalf("services: %+v", view.Status.Services)
	}

	// Actions for node become available once package.json exists.
	infos, err := e.m.ListActions(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range infos {
		if i.ID == "npm:install" && (i.Available || !strings.Contains(i.Reason, "package.json")) {
			t.Fatalf("npm:install availability: %+v", i)
		}
	}

	// Version change recreates the container; removal deletes it.
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24"}})
	if err != nil {
		t.Fatal(err)
	}
	node, _ = e.engine.Container("staqio-front-node")
	if node.Spec.Image != "ghcr.io/seramos/staqio-node:24" || node.State != "running" {
		t.Fatalf("after version change: %+v", node)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "9"}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown version must be rejected, got %v", err)
	}
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("staqio-front-node"); ok || view.Project.Service(store.ServiceNode) != nil || len(view.Status.Services) != 2 {
		t.Fatalf("node must be removed: %+v", view.Status)
	}

	// Add later.
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	node, ok = e.engine.Container("staqio-front-node")
	if !ok || node.State != "running" || node.Spec.Image != "ghcr.io/seramos/staqio-node:24" {
		t.Fatalf("node added later must use the default version and run: %+v", node)
	}
}

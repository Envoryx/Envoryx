package project

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Stopping a project also stops a container that keeps restarting after a crash; Docker's
// restart policy would bring it back forever otherwise.
func TestStopStopsARestartingContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Loop", true))
	if err != nil {
		t.Fatal(err)
	}
	if !e.engine.SetState("envoryx-loop-php", "restarting") {
		t.Fatal("no php container")
	}
	if _, err := e.m.Stop(ctx, v.Project.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-loop-php"); c.State != "exited" {
		t.Fatalf("the restarting container is %s", c.State)
	}
}

// A container that lost the project network is recreated on the next start instead of
// being restarted without it.
func TestStartRecreatesAContainerThatLostTheNetwork(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Net", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.engine.DisconnectNetwork(ctx, "envoryx-net", "envoryx-net-web"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restart(ctx, v.Project.ID); err != nil {
		t.Fatal(err)
	}
	nets, err := e.engine.ContainerNetworks(ctx, "envoryx-net-web")
	if err != nil || !slices.Contains(nets, "envoryx-net") {
		t.Fatalf("web after the restart: %v %v", nets, err)
	}
}

// The reconciler reports a running project whose network is gone.
func TestReconcileReportsALostNetwork(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.Create(ctx, phpRequest("Gone", true)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"envoryx-gone-web", "envoryx-gone-php"} {
		_ = e.engine.DisconnectNetwork(ctx, "envoryx-gone", c)
	}
	if err := e.engine.RemoveNetwork(ctx, "envoryx-gone"); err != nil {
		t.Fatal(err)
	}
	report := e.m.Reconcile(ctx)
	if !slices.ContainsFunc(report.Issues, func(i ReconcileIssue) bool { return strings.Contains(i.Message, "project network is gone") }) {
		t.Fatalf("issues: %+v", report.Issues)
	}
}

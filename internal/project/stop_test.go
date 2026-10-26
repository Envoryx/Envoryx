package project

import (
	"context"
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

package project

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

// A command runs in the project's application container with the tooling environment the
// terminal also gets, and its exit code and streams come back separately.
func TestExecReturnsExitCodeAndStreams(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	var gotEnv []string
	var gotStdin string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		gotEnv, gotStdin = env, string(stdin)
		if !slices.Equal(cmd, []string{"composer", "install"}) {
			t.Errorf("cmd = %v", cmd)
		}
		return "installed\n", 2, nil
	}
	var out, errOut bytes.Buffer
	code, err := e.m.Exec(ctx, view.Project.ID, store.ServicePHP, ExecOptions{
		Cmd:    []string{"composer", "install"},
		Stdin:  strings.NewReader("yes\n"),
		Stdout: &out,
		Stderr: &errOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if out.String() != "installed\n" || errOut.Len() != 0 {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	if gotStdin != "yes\n" {
		t.Fatalf("stdin = %q", gotStdin)
	}
	if !slices.Contains(gotEnv, "CI=1") {
		t.Fatalf("env misses CI=1: %v", gotEnv)
	}
	if !slices.Contains(gotEnv, "HOME="+homeMountTarget) {
		t.Fatalf("env misses the tooling home: %v", gotEnv)
	}
	// The audit log names the service and the command, like the terminal does.
	entries, err := e.store.Audit.Recent(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Action == "project.exec" {
			found = true
		}
	}
	if !found {
		t.Fatal("no project.exec entry in the audit log")
	}
}

// A stopped project has no container to run in, and an empty command is refused before
// anything is contacted.
func TestExecNeedsARunningContainerAndACommand(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Exec(ctx, view.Project.ID, store.ServicePHP, ExecOptions{Cmd: []string{"ls"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("exec into a stopped project: %v", err)
	}
	if _, err := e.m.Exec(ctx, view.Project.ID, store.ServicePHP, ExecOptions{}); err == nil {
		t.Fatal("exec without a command was accepted")
	}
	if _, err := e.m.Exec(ctx, view.Project.ID, store.ServiceRedis, ExecOptions{Cmd: []string{"redis-cli"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exec into an absent service: %v", err)
	}
}

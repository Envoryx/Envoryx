package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

const brokenKernel = "6.19.0-31-generic"

func TestMongoVersionsOnAffectedKernel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Set up on an older kernel, where 7.0 and 8.0 still start.
	e.engine.KernelVersion = "6.8.0-1-pve"
	req := phpRequest("Shop", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "7"}
	req.Databases = []NamedDatabaseRequest{{Name: "analytics", DatabaseRequest: DatabaseRequest{Type: "mongodb", Version: "8"}}}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Status.Warnings) != 0 {
		t.Fatalf("warnings on an older kernel: %v", view.Status.Warnings)
	}

	// The host kernel moves to 6.19: both databases are named with what to switch to.
	e.engine.KernelVersion = brokenKernel
	e.m.noteKernel(brokenKernel)
	want := []string{
		"cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); switch to MongoDB 8.2",
		"cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); switch to MongoDB 8.2",
	}
	view, err = e.m.Get(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !slices.Contains(view.Status.Warnings, w) {
			t.Fatalf("missing %q in %v", w, view.Status.Warnings)
		}
	}
	views, err := e.m.List(ctx)
	if err != nil || len(views) != 1 || !slices.Contains(views[0].Status.Warnings, want[0]) {
		t.Fatalf("list warnings: %v %v", views, err)
	}

	// The project keeps its versions: an unrelated change still saves.
	env := []EnvVarRequest{{Key: "APP_ENV", Value: "production"}}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Env: &env}); err != nil {
		t.Fatalf("unrelated update refused: %v", err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Type: "mongodb", Version: "7", ExposePort: true}}); err != nil {
		t.Fatalf("update keeping the version refused: %v", err)
	}

	// Newly choosing a broken version is refused, with the reason.
	_, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Databases: map[string]DatabaseUpdate{"reports": {Enabled: true, Type: "mongodb", Version: "8"}}})
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "cannot start MongoDB 8.0 on Linux kernel 6.19") {
		t.Fatalf("adding mongo 8.0: %v", err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Databases: map[string]DatabaseUpdate{"reports": {Enabled: true, Type: "mongodb", Version: "8.2"}}}); err != nil {
		t.Fatalf("adding mongo 8.2: %v", err)
	}
	req = phpRequest("Blog", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	if _, err := e.m.Preview(ctx, req); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("preview with mongo 8.0: %v", err)
	}
	if _, err := e.m.Create(ctx, req); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "switch to MongoDB 8.2") {
		t.Fatalf("create with mongo 8.0: %v", err)
	}
	req.Database.Version = "8.2"
	if _, err := e.m.Create(ctx, req); err != nil {
		t.Fatalf("create with mongo 8.2: %v", err)
	}
}

func TestUnknownKernelLocksNothing(t *testing.T) {
	e := newEnv(t)
	req := phpRequest("Shop", false)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	view, err := e.m.Create(context.Background(), req)
	if err != nil || len(view.Status.Warnings) != 0 {
		t.Fatalf("unknown kernel: %v %v", view.Status.Warnings, err)
	}
}

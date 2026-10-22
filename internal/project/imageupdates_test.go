package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/runtime"
)

// hasWarning reports whether one of the warnings contains part.
func hasWarning(warnings []string, part string) bool {
	return slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, part) })
}

// A rebuilt image in the registry shows on the projects that use it and is announced
// once; the restart that pulls it clears both.
func TestImageCheckFindsNewerBuilds(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	php := v.Project.Service("php").Image

	if got := e.m.CheckImageUpdates(ctx); len(got.Newer) != 0 || got.CheckedAt == nil {
		t.Fatalf("nothing is newer right after the create: %+v", got)
	}
	e.engine.Remote[php] = php + "@v2"
	if got := e.m.CheckImageUpdates(ctx); !slices.Equal(got.Newer, []string{php}) {
		t.Fatalf("the rebuilt php image must be found: %+v", got)
	}
	got, err := e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(got.Status.Warnings, "a newer php image is available; restart to apply") {
		t.Fatalf("the project must say so: %v", got.Status.Warnings)
	}
	if s := got.Status.Services[0]; s.Kind != "php" || s.ImageUpdate != "available" {
		t.Fatalf("the php service must be marked: %+v", s)
	}
	newer := func() int {
		n := 0
		for _, k := range sender.kinds() {
			if strings.HasPrefix(k, "images.newer:") {
				n++
			}
		}
		return n
	}
	if newer() != 1 {
		t.Fatalf("one notification expected: %v", sender.kinds())
	}
	e.m.CheckImageUpdates(ctx)
	if newer() != 1 {
		t.Fatalf("the same build must not be announced twice: %v", sender.kinds())
	}

	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := e.m.CheckImageUpdates(ctx); len(got.Newer) != 0 {
		t.Fatalf("after the restart the host has the build: %+v", got)
	}
	got, _ = e.m.Get(ctx, id)
	if hasWarning(got.Status.Warnings, "newer php image") {
		t.Fatalf("no warning after the restart: %v", got.Status.Warnings)
	}

	// The next rebuild is news again.
	e.engine.Remote[php] = php + "@v3"
	e.m.CheckImageUpdates(ctx)
	if newer() != 2 {
		t.Fatalf("a further rebuild must be announced: %v", sender.kinds())
	}
}

// A registry the host can't ask leaves the others checked and says why.
func TestImageCheckReportsFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, shopRequest())
	if err != nil {
		t.Fatal(err)
	}
	php := v.Project.Service("php").Image
	db := v.Project.Service("database").Image
	e.engine.Remote[php] = php + "@v2"
	e.engine.FailRemote = map[string]error{db: errors.New("403 Forbidden")}
	got := e.m.CheckImageUpdates(ctx)
	if !slices.Contains(got.Newer, php) || !strings.Contains(got.Error, db) || !strings.Contains(got.Error, "403 Forbidden") {
		t.Fatalf("php must be found and the database named: %+v", got)
	}
}

// With the restart switched on, the running projects with a newer image are restarted
// once in their hour, a stopped one stays stopped, and a missed slot doesn't run in the
// afternoon.
func TestImageAutoRestart(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	running, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Stopped", false)); err != nil {
		t.Fatal(err)
	}
	php := running.Project.Service("php").Image
	e.engine.Remote[php] = php + "@v2"
	at := func(h, m int) time.Time { return time.Date(2026, 10, 23, h, m, 0, 0, time.Local) }

	// Off by default.
	e.m.restartForImages(ctx, at(5, 1), e.m.log)
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ImageID == php+"@v2" {
		t.Fatal("nothing restarts while the setting is off")
	}

	if err := e.m.SetImageUpdates(ctx, ImageUpdateSettings{AutoRestart: true, Hour: 24}); err == nil {
		t.Fatal("hour 24 must be refused")
	}
	if err := e.m.SetImageUpdates(ctx, ImageUpdateSettings{AutoRestart: true, Hour: 5}); err != nil {
		t.Fatal(err)
	}
	e.m.restartForImages(ctx, at(4, 59), e.m.log)
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ImageID == php+"@v2" {
		t.Fatal("before the hour nothing restarts")
	}
	e.m.restartForImages(ctx, at(5, 1), e.m.log)
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ImageID != php+"@v2" {
		t.Fatalf("the running project must run the new build, got %s", c.ImageID)
	}
	if _, ok := e.engine.Container("envoryx-stopped-php"); ok {
		if c, _ := e.engine.Container("envoryx-stopped-php"); c.State == "running" {
			t.Fatal("a stopped project must stay stopped")
		}
	}
	if !slices.Contains(sender.kinds(), "images.restarted:1 of 1 project restarted for newer images") {
		t.Fatalf("the restart must be reported: %v", sender.kinds())
	}

	// The slot runs once, and the next day's only within its hour.
	e.engine.Remote[php] = php + "@v3"
	e.m.restartForImages(ctx, at(5, 30), e.m.log)
	e.m.restartForImages(ctx, at(5, 0).AddDate(0, 0, 1).Add(2*time.Hour), e.m.log)
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ImageID != php+"@v2" {
		t.Fatalf("no second restart in the same slot or outside the hour, got %s", c.ImageID)
	}
}

// A version past its end of life says so on the project.
func TestEndOfLifeVersionWarns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Legacy", false)
	req.PHP = &PHPRequest{Version: "8.1", Config: runtime.DefaultPHPConfig()}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(v.Status.Warnings, "PHP 8.1 has reached its end of life and gets no security fixes; switch to a newer version") {
		t.Fatalf("an EOL version must warn: %v", v.Status.Warnings)
	}
	cur, _ := e.m.Create(ctx, phpRequest("Current", false))
	if hasWarning(cur.Status.Warnings, "end of life") {
		t.Fatalf("a supported version must not: %v", cur.Status.Warnings)
	}
}

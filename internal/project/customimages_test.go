package project

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// probeAll answers the image check as if every program were there.
func probeAll(spec docker.ContainerSpec) (docker.ExecResult, error) {
	if len(spec.Entrypoint) == 0 || spec.Entrypoint[0] != "sh" {
		return docker.ExecResult{}, nil
	}
	var out []string
	for _, part := range strings.Split(spec.Cmd[0], ";") {
		if f := strings.Fields(part); len(f) > 2 && f[0] == "command" {
			out = append(out, f[2])
		}
	}
	return docker.ExecResult{Stdout: strings.Join(out, "\n") + "\next:xdebug\n"}, nil
}

func contextNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

func TestCustomImageFromRegistry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Reg", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	// An image without socat, Composer and Xdebug, with an entrypoint of its own.
	ref := "ghcr.io/acme/php:8.4"
	e.engine.ImageInfos[ref] = docker.ImageInfo{Entrypoint: []string{"/start.sh"}}
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		return docker.ExecResult{Stdout: "sh\ngit\nssh\nphp\nphp-fpm\n"}, nil
	}
	view, err = e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{Image: ref})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-reg-php")
	if !ok || c.Spec.Image != ref {
		t.Fatalf("the running project must use the image: %+v", c.Spec.Image)
	}
	svc := view.Project.Service(store.ServicePHP)
	w := strings.Join(svc.Custom.Warnings, "|")
	for _, want := range []string{"socat is missing", "composer is missing", "Xdebug", "ENTRYPOINT /start.sh", "extension switches"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q missing in %q", want, w)
		}
	}
	if svc.Custom.CheckedImage != ref || svc.Custom.CheckedAt.IsZero() {
		t.Fatalf("check: %+v", svc.Custom)
	}
	// Git and templates keep the catalogue image, whose tools Envoryx relies on.
	if img, _ := e.m.toolImage(view.Project); img != "ghcr.io/envoryx/envoryx-php:8.4" {
		t.Fatalf("tool image: %s", img)
	}

	// An Envoryx-based image of another runtime is named as such.
	e.engine.ImageInfos["ghcr.io/acme/node:1"] = docker.ImageInfo{Labels: map[string]string{labelRuntime: "node"}}
	e.engine.OneShotHandler = probeAll
	view, err = e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{Image: "ghcr.io/acme/node:1"})
	if err != nil {
		t.Fatal(err)
	}
	if w := view.Project.Service(store.ServicePHP).Custom.Warnings; len(w) != 1 || !strings.Contains(w[0], "Envoryx's node image") {
		t.Fatalf("warnings: %q", w)
	}

	// Back to the catalogue.
	if view, err = e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-reg-php"); c.Spec.Image != "ghcr.io/envoryx/envoryx-php:8.4" || view.Project.Service(store.ServicePHP).Custom.Set() {
		t.Fatalf("catalogue image: %s", c.Spec.Image)
	}

	for _, bad := range []CustomImageRequest{
		{Image: "a b"}, {Image: "envoryx-build/php:x"}, {Dockerfile: "../Dockerfile"}, {Dockerfile: "/etc/Dockerfile"},
		{Dockerfile: "a/./Dockerfile"}, {Image: "php:8", Dockerfile: "Dockerfile"},
	} {
		if _, err := e.m.SetCustomImage(ctx, id, store.ServicePHP, bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := e.m.SetCustomImage(ctx, id, store.ServiceWeb, CustomImageRequest{Image: "caddy:2"}); !errors.Is(err, validate.ErrInvalid) {
		t.Errorf("web servers keep their image: %v", err)
	}
}

func TestCustomImageFromDockerfile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = probeAll
	view, err := e.m.Create(ctx, phpRequest("Built", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	dir := filepath.Join(e.projDir, view.Project.Path, ".envoryx")
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("php.Dockerfile", "FROM ghcr.io/envoryx/envoryx-php:8.4\nCOPY conf/ /usr/local/etc/php/conf.d/\n")
	write("conf/extra.ini", "memory_limit=1G\n")
	// A symlink out of the context is not sent along.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")); err != nil {
		t.Fatal(err)
	}
	var sent []string
	e.engine.BuildHandler = func(opts docker.BuildOptions, context []byte) error {
		sent = contextNames(t, context)
		opts.Output("Step 1/2 : FROM ghcr.io/envoryx/envoryx-php:8.4")
		opts.Output("Successfully built")
		return nil
	}

	view, err = e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{Dockerfile: ".envoryx/php.Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.engine.Builds) != 1 {
		t.Fatalf("builds: %+v", e.engine.Builds)
	}
	b := e.engine.Builds[0]
	if !strings.HasPrefix(b.Tag, "envoryx-build/php:") || b.Dockerfile != "php.Dockerfile" || b.Labels[labelBuild] != "php" || b.Pull || b.NoCache {
		t.Fatalf("build: %+v", b)
	}
	if strings.Join(sent, ",") != "conf/,conf/extra.ini,php.Dockerfile" {
		t.Fatalf("context: %v", sent)
	}
	svc := view.Project.Service(store.ServicePHP)
	if svc.Image != b.Tag || !strings.Contains(svc.Custom.BuildOutput, "Successfully built") || svc.Custom.BuildFailed || svc.Custom.CheckedImage != b.Tag {
		t.Fatalf("service: %s %+v", svc.Image, svc.Custom)
	}
	if c, _ := e.engine.Container("envoryx-built-php"); c.Spec.Image != b.Tag {
		t.Fatalf("container image: %s", c.Spec.Image)
	}

	// A restart does not build again (and does not try to pull the local tag) ...
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.Builds) != 1 {
		t.Fatalf("unchanged context rebuilt: %d", len(e.engine.Builds))
	}
	for _, c := range e.engine.Calls {
		if c == "pull:"+b.Tag {
			t.Fatal("a built tag was pulled")
		}
	}
	// ... a changed file next to the Dockerfile gives a new tag, which the start builds.
	write("conf/extra.ini", "memory_limit=2G\n")
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.Builds) != 2 || e.engine.Builds[1].Tag == b.Tag {
		t.Fatalf("changed context: %+v", e.engine.Builds)
	}
	// The old build is no longer used and can be pruned (like the catalogue image).
	unused, _ := e.m.UnusedImages(ctx)
	var tags []string
	for _, u := range unused {
		tags = append(tags, u.Tags...)
	}
	if strings.Join(tags, ",") != b.Tag+",ghcr.io/envoryx/envoryx-php:8.4" {
		t.Fatalf("unused: %v", tags)
	}

	// A rebuild pulls the base image and ignores the cache.
	if _, err := e.m.RebuildCustomImage(ctx, id, store.ServicePHP); err != nil {
		t.Fatal(err)
	}
	if last := e.engine.Builds[len(e.engine.Builds)-1]; !last.Pull || !last.NoCache {
		t.Fatalf("rebuild: %+v", last)
	}

	// A failing build keeps the setting, reports the output and fails the start.
	write("php.Dockerfile", "FROM nothing\nRUN false\n")
	e.engine.BuildHandler = func(opts docker.BuildOptions, _ []byte) error {
		opts.Output("Step 2/2 : RUN false")
		return errors.New("The command '/bin/sh -c false' returned a non-zero code: 1")
	}
	if _, err := e.m.Restart(ctx, id); err == nil || !strings.Contains(err.Error(), "non-zero code") {
		t.Fatalf("failed build: %v", err)
	}
	// The build ran before the containers stopped: the project keeps running.
	if c, ok := e.engine.Container("envoryx-built-php"); !ok || c.State != "running" {
		t.Fatalf("the failed build stopped the project: %+v", c.Spec.Image)
	}
	got, _ := e.m.Get(ctx, id)
	if c := got.Project.Service(store.ServicePHP).Custom; !c.BuildFailed || !strings.Contains(c.BuildOutput, "RUN false") || c.Dockerfile == "" {
		t.Fatalf("recorded: %+v", c)
	}

	// A missing Dockerfile is named when the image is needed.
	if err := os.Remove(filepath.Join(dir, "php.Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restart(ctx, id); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing Dockerfile: %v", err)
	}
}

func TestCustomImageInManifestAndCopies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = probeAll
	view, err := e.m.Create(ctx, phpRequest("Mf", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	mf, err := e.m.ExportManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if mf.PHP.Image != "" || mf.PHP.Dockerfile != "" {
		t.Fatalf("export: %+v", mf.PHP)
	}
	mf.PHP.Image = "ghcr.io/acme/php:8.4"
	plan, err := e.m.PlanManifest(ctx, id, mf, ManifestOptions{})
	if err != nil || len(plan.Changes) != 1 || !strings.Contains(plan.Changes[0].To, "image: ghcr.io/acme/php:8.4") {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if _, err := e.m.ApplyManifest(ctx, id, mf, ManifestOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.m.Get(ctx, id)
	if got.Project.Service(store.ServicePHP).Custom.Image != "ghcr.io/acme/php:8.4" {
		t.Fatalf("applied: %+v", got.Project.Service(store.ServicePHP).Custom)
	}
	if again, _ := e.m.ExportManifest(ctx, id); again.PHP.Image != "ghcr.io/acme/php:8.4" {
		t.Fatalf("round trip: %+v", again.PHP)
	}
	bad := mf
	php := *mf.PHP
	php.Dockerfile = "Dockerfile"
	bad.PHP = &php
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both: %v", err)
	}

	// A copy runs the same image.
	cp, err := e.m.Duplicate(ctx, id, DuplicateRequest{Name: "Mf Copy"})
	if err != nil {
		t.Fatal(err)
	}
	if c := cp.Project.Service(store.ServicePHP); c.Custom.Image != "ghcr.io/acme/php:8.4" || c.Image != "ghcr.io/acme/php:8.4" {
		t.Fatalf("copy: %s %+v", c.Image, c.Custom)
	}
}

func TestRegistryLogins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	regs, err := e.m.SetRegistries(ctx, []Registry{
		{Host: "https://GHCR.io/", Username: "me", Password: "tok"},
		{Host: "index.docker.io", Username: "hub", Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 || regs[0].Host != "ghcr.io" || regs[1].Host != "docker.io" || regs[0].Password != "" || !regs[0].HasPassword {
		t.Fatalf("stored: %+v", regs)
	}
	// The engine gets the passwords; an update without one keeps it.
	if _, err := e.m.SetRegistries(ctx, []Registry{{Host: "ghcr.io", Username: "me2"}}); err != nil {
		t.Fatal(err)
	}
	creds := e.engine.Credentials()
	if len(creds) != 1 || creds[0] != (docker.RegistryCredential{Host: "ghcr.io", Username: "me2", Password: "tok"}) {
		t.Fatalf("engine: %+v", creds)
	}
	for _, bad := range [][]Registry{
		{{Host: "", Username: "a", Password: "b"}},
		{{Host: "ghcr.io", Password: "b"}},
		{{Host: "new.example.com", Username: "a"}},
		{{Host: "a.io", Username: "a", Password: "b"}, {Host: "A.io", Username: "a", Password: "b"}},
	} {
		if _, err := e.m.SetRegistries(ctx, bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

// A start that failed with an earlier image stored its error; one that works with the
// next image clears it, as a plain start would.
func TestCustomImageClearsTheErrorOfAnEarlierStart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Retry", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if err := e.store.Projects.UpdateState(ctx, id, store.DesiredRunning, store.LifecycleReady, "start php: the image broke"); err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/acme/php:8.4"
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		return docker.ExecResult{Stdout: "sh\ngit\nssh\nphp\nphp-fpm\nsocat\ncomposer\n"}, nil
	}
	view, err = e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{Image: ref})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Projects.Get(ctx, id); got.LastError != "" {
		t.Fatalf("the old error stayed: %q", got.LastError)
	}
}

package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/validate"
)

const widgetAddon = `
name: widget
title: Widget
versions:
  - version: "1"
    image: acme/widget:1
    default: true
  - version: "2"
    image: acme/widget:2
port: 8080
webUI: true
command: ["serve", "--name", "{{project.slug}}"]
env:
  WIDGET_PASSWORD: "{{secret.password}}"
  WIDGET_DB: "{{database.host}}:{{database.port}}"
secrets: [password]
volumes:
  - name: data
    path: /data
  - name: cache
    path: /cache
    noBackup: true
inject:
  WIDGET_URL: "http://{{host}}:{{port}}"
  ENVORYX_PROJECT: hijacked
credentials:
  - label: Password
    value: "{{secret.password}}"
    secret: true
`

func specEnv(spec docker.ContainerSpec, key string) string {
	for _, kv := range spec.Env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	return ""
}

func TestAddonLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.InstallAddon(ctx, []byte(widgetAddon)); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, phpRequest("Gadget", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"nope": {Enabled: true}}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("not installed: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"widget": {Enabled: true}}}); err != nil {
		t.Fatal(err)
	}

	c, ok := e.engine.Container("envoryx-gadget-addon-widget")
	if !ok {
		t.Fatalf("no addon container: %v", e.engine.ContainerNames())
	}
	s := c.Spec
	pw := specEnv(s, "WIDGET_PASSWORD")
	if len(pw) != 24 || specEnv(s, "WIDGET_DB") != ":" || s.Image != "acme/widget:1" {
		t.Fatalf("spec env/image: %v %s", s.Env, s.Image)
	}
	if strings.Join(s.Cmd, " ") != "serve --name gadget" || !slices.Equal(s.NetworkAlias, []string{"widget"}) {
		t.Fatalf("cmd/alias: %v %v", s.Cmd, s.NetworkAlias)
	}
	if len(s.Mounts) != 2 || s.Mounts[0].Source != "envoryx-gadget-addon-widget-data" || s.Mounts[0].Target != "/data" {
		t.Fatalf("mounts: %+v", s.Mounts)
	}
	if len(s.Ports) != 1 || s.Ports[0].ContainerPort != 8080 || s.Ports[0].HostPort == 0 {
		t.Fatalf("a web UI is published: %+v", s.Ports)
	}
	// The application gets the injected variables.
	if php, _ := e.engine.Container("envoryx-gadget-php"); specEnv(php.Spec, "WIDGET_URL") != "http://widget:8080" || specEnv(php.Spec, "ENVORYX_PROJECT") != "gadget" {
		t.Fatalf("injected: %v", php.Spec.Env)
	}
	// The proxy routes the web UI.
	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	if _, ok := table.Routes[AddonHostname("gadget", "widget", DefaultBaseDomain)]; !ok {
		t.Fatalf("route missing: %v", table.Routes)
	}
	infos, err := e.m.ProjectAddons(ctx, id)
	if err != nil || len(infos) != 1 {
		t.Fatalf("infos: %+v %v", infos, err)
	}
	if in := infos[0]; in.Credentials[0].Value != pw || !in.Installed || in.State != "running" || len(in.Volumes) != 2 || !slices.Equal(in.InjectedEnv, []string{"ENVORYX_PROJECT", "WIDGET_URL"}) {
		t.Fatalf("info: %+v", in)
	}

	// A new version of the file reaches the project's copy; the secret stays.
	if _, err := e.m.InstallAddon(ctx, []byte(strings.Replace(widgetAddon, "WIDGET_URL", "WIDGET_ENDPOINT", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-gadget-addon-widget")
	if specEnv(c.Spec, "WIDGET_PASSWORD") != pw {
		t.Fatal("the generated secret changed")
	}
	if php, _ := e.engine.Container("envoryx-gadget-php"); specEnv(php.Spec, "WIDGET_ENDPOINT") == "" {
		t.Fatalf("updated definition: %v", php.Spec.Env)
	}
	// Changing the version.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"widget": {Enabled: true, Version: "2"}}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-gadget-addon-widget"); c.Spec.Image != "acme/widget:2" {
		t.Fatalf("version: %s", c.Spec.Image)
	}

	// The file of an addon in use cannot go.
	if err := e.m.DeleteAddon(ctx, "widget"); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete in use: %v", err)
	}
	// Removing asks for the data first.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"widget": {}}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("remove without removeData: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"widget": {RemoveData: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-gadget-addon-widget"); ok {
		t.Fatal("container left")
	}
	for _, v := range e.engine.VolumeNames() {
		if strings.Contains(v, "addon-widget") {
			t.Fatalf("volume left: %s", v)
		}
	}
	if php, _ := e.engine.Container("envoryx-gadget-php"); specEnv(php.Spec, "WIDGET_ENDPOINT") != "" {
		t.Fatal("the injected variable stays after removal")
	}
	if err := e.m.DeleteAddon(ctx, "widget"); err != nil {
		t.Fatal(err)
	}
}

func TestAddonManifestAndBackup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.InstallAddon(ctx, []byte(widgetAddon)); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, phpRequest("Mfa", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	mf, err := e.m.ExportManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	mf.Addons = map[string]manifest.Addon{"widget": {}}
	plan, err := e.m.PlanManifest(ctx, id, mf, ManifestOptions{})
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Section != "addons.widget" || plan.Changes[0].Action != "add" {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if _, err := e.m.ApplyManifest(ctx, id, mf, ManifestOptions{}); err != nil {
		t.Fatal(err)
	}
	again, _ := e.m.ExportManifest(ctx, id)
	if a, ok := again.Addons["widget"]; !ok || a.Version != "1" {
		t.Fatalf("export: %+v", again.Addons)
	}
	if plan, _ := e.m.PlanManifest(ctx, id, again, ManifestOptions{}); plan.Pending() {
		t.Fatalf("round trip not clean: %+v", plan.Changes)
	}
	mf.Addons = map[string]manifest.Addon{"missing": {}}
	if _, err := e.m.PlanManifest(ctx, id, mf, ManifestOptions{}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("uninstalled addon: %v", err)
	}

	// A database backup archives the addon volume (not the cache) with the addon stopped,
	// and a restore unpacks it.
	var archived, unpacked []string
	e.engine.OneShotStreamHandler = func(spec docker.ContainerSpec, stdin []byte) (string, int, error) {
		if spec.Image != addonVolumeTool {
			return "", 0, nil
		}
		if c, _ := e.engine.Container("envoryx-mfa-addon-widget"); c.State == "running" {
			t.Error("the addon runs while its volume is read")
		}
		if spec.Cmd[0] == "tar" {
			archived = append(archived, spec.Mounts[0].Source)
			return "TARBYTES", 0, nil
		}
		unpacked = append(unpacked, spec.Mounts[0].Source+":"+string(stdin))
		return "", 0, nil
	}
	b, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(archived, []string{"envoryx-mfa-addon-widget-data"}) || !slices.Equal(b.Meta.AddonVolumes, []string{"addon-widget-data.tar.gz"}) {
		t.Fatalf("backup: %v %v", archived, b.Meta.AddonVolumes)
	}
	if c, _ := e.engine.Container("envoryx-mfa-addon-widget"); c.State != "running" {
		t.Fatal("the addon was not started again")
	}
	e.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) { return "", 0, nil }
	if _, err := e.m.RestoreBackup(ctx, id, b.ID, RestoreOptions{Database: true, Confirm: "mfa"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unpacked, []string{"envoryx-mfa-addon-widget-data:TARBYTES"}) {
		t.Fatalf("restore: %v", unpacked)
	}
}

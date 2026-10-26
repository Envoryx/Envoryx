package project

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
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
	// The web UI's port is the proxy's way in, on 127.0.0.1 only: in the LAN it would
	// skip the project's sign-in and rules.
	if len(s.Ports) != 1 || s.Ports[0].ContainerPort != 8080 || s.Ports[0].HostPort == 0 || s.Ports[0].HostIP != "127.0.0.1" {
		t.Fatalf("a web UI is published on loopback: %+v", s.Ports)
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
	// It signs in through Envoryx and keeps the project's allowlist; the basic
	// authentication and the redirects are the application's.
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{AllowIPs: []string{"10.1.2.3"}, BasicAuth: &BasicAuthRequest{User: "client", Password: "s3cret"}}); err != nil {
		t.Fatal(err)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	route := table.Routes[AddonHostname("gadget", "widget", DefaultBaseDomain)]
	if r := route.Rules; route.Login != LoginAddon || r == nil || len(r.Allow) != 1 || r.BasicAuth != nil || len(r.Redirects) != 0 {
		t.Fatalf("addon route: %+v rules %+v", route, r)
	}
	infos, err := e.m.ProjectAddons(ctx, id)
	if err != nil || len(infos) != 1 {
		t.Fatalf("infos: %+v %v", infos, err)
	}
	// ENVORYX_PROJECT is Envoryx's own: the card says the addon's value doesn't reach the app.
	if in := infos[0]; in.Credentials[0].Value != pw || !in.Installed || in.State != "running" || len(in.Volumes) != 2 || !slices.Equal(in.InjectedEnv, []string{"WIDGET_URL"}) || !slices.Equal(in.ShadowedEnv, []string{"ENVORYX_PROJECT"}) {
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

// An addon file edited by hand under /config/addons reaches the project at its next
// restart, like one installed through the API; a start with an unchanged file leaves the
// project's copy alone.
func TestHandEditedAddonFileReachesTheProjectOnRestart(t *testing.T) {
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
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"widget": {Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	before, _ := e.store.Projects.Get(ctx, id)
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if after, _ := e.store.Projects.Get(ctx, id); string(after.Service(store.AddonKind("widget")).Config) != string(before.Service(store.AddonKind("widget")).Config) {
		t.Fatal("an unchanged file rewrote the project's copy")
	}

	dir, err := e.m.addonsDir()
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(widgetAddon, `"serve", "--name"`, `"serve", "--title"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "widget.yml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-gadget-addon-widget"); strings.Join(c.Spec.Cmd, " ") != "serve --title gadget" {
		t.Fatalf("command after the edit: %v", c.Spec.Cmd)
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
	// The download and the offsite copy carry the volume archive too.
	rc, _, err := e.m.OpenBackupArchive(ctx, id, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var members []string
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		members = append(members, h.Name)
	}
	rc.Close()
	if !slices.ContainsFunc(members, func(n string) bool { return strings.HasSuffix(n, "addon-widget-data.tar.gz") }) {
		t.Fatalf("download without the addon volume: %v", members)
	}
	e.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) { return "", 0, nil }
	if _, err := e.m.RestoreBackup(ctx, id, b.ID, RestoreOptions{Database: true, Confirm: "mfa"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unpacked, []string{"envoryx-mfa-addon-widget-data:TARBYTES"}) {
		t.Fatalf("restore: %v", unpacked)
	}
}

// An addon that injects nothing lists an empty array, not null: the UI reads its length.
func TestAddonWithoutInjectListsEmpty(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.InstallAddon(ctx, []byte("name: plain\ntitle: Plain\nversions:\n  - version: \"1\"\n    image: acme/plain:1\n    default: true\nport: 80\n")); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, phpRequest("Gadget", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Addons: map[string]AddonUpdate{"plain": {Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	infos, err := e.m.ProjectAddons(ctx, view.Project.ID)
	if err != nil || len(infos) != 1 || infos[0].InjectedEnv == nil {
		t.Fatalf("infos: %+v %v", infos, err)
	}
}

func TestPublicAddonStaysOpen(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const ws = "name: ws\nversions:\n  - {version: \"1\", image: acme/ws:1}\nport: 6001\nwebUI: true\npublic: true\n"
	if _, err := e.m.InstallAddon(ctx, []byte(ws)); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, phpRequest("Chat", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"ws": {Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{BasicAuth: &BasicAuthRequest{User: "client", Password: "s3cret"}}); err != nil {
		t.Fatal(err)
	}
	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	route := table.Routes[AddonHostname("chat", "ws", DefaultBaseDomain)]
	if route.Login != "" || route.Rules == nil || route.Rules.BasicAuth == nil {
		t.Fatalf("a public addon is guarded like the application: %+v", route)
	}
}

// A port the admin chose to publish listens like the project's ports; a web UI's stays
// on loopback, behind the proxy.
func TestExposedAddonPortListensLikeProjectPorts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const db = `name: tinydb
title: Tiny DB
versions:
  - version: "1"
    image: acme/tinydb:1
port: 7000
publishPort: true
`
	if _, err := e.m.InstallAddon(ctx, []byte(db)); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, phpRequest("Gadget", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Addons: map[string]AddonUpdate{"tinydb": {Enabled: true, ExposePort: true}}}); err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-gadget-addon-tinydb")
	if !ok {
		t.Fatalf("no addon container: %v", e.engine.ContainerNames())
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].HostIP == "127.0.0.1" {
		t.Fatalf("an exposed port listens like the project's: %+v", c.Spec.Ports)
	}
	infos, err := e.m.ProjectAddons(ctx, view.Project.ID)
	if err != nil || len(infos) != 1 || infos[0].HostPortLocal {
		t.Fatalf("info: %+v %v", infos, err)
	}
}

// Health checks written as in a Compose file run as such: CMD-SHELL through sh -c, CMD
// without the prefix, NONE not at all.
func TestAddonHealthTest(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"CMD-SHELL", "wget -qO- http://127.0.0.1/ || exit 1"}, []string{"sh", "-c", "wget -qO- http://127.0.0.1/ || exit 1"}},
		{[]string{"CMD", "pg_isready", "-q"}, []string{"pg_isready", "-q"}},
		{[]string{"redis-cli", "ping"}, []string{"redis-cli", "ping"}},
		{[]string{"NONE"}, nil},
	} {
		if got := addonHealthTest(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("%v: %v, want %v", tc.in, got, tc.want)
		}
	}
}

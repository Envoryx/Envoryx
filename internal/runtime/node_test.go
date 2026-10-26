package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNodePresetDefaults(t *testing.T) {
	cases := []struct {
		preset string
		port   int
		want   int
	}{
		{"vite", 0, 5173},
		{"next", 0, 3000},
		{"nuxt", 0, 3000},
		{"generic", 0, 5173},
		{"", 0, 5173},
		{"nuxt", 4000, 4000}, // an explicit port wins over the preset default
	}
	for _, tc := range cases {
		c := NodeConfig{DevServer: true, Preset: tc.preset, Port: tc.port}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%s: %v", tc.preset, err)
		}
		if c.Port != tc.want {
			t.Errorf("preset %q port %d: got %d, want %d", tc.preset, tc.port, c.Port, tc.want)
		}
	}
	bad := NodeConfig{DevServer: true, Preset: "astro"}
	if err := bad.Normalize(); err == nil {
		t.Fatal("unknown preset must fail")
	}
	if _, ok := NodePresetByKey("nuxt"); !ok {
		t.Fatal("NodePresetByKey(nuxt)")
	}
	if _, ok := NodePresetByKey("astro"); ok {
		t.Fatal("NodePresetByKey must not know astro")
	}
	if NodePresets[0].Key != "vite" || len(NodePresets) != 4 {
		t.Fatalf("presets: %+v", NodePresets)
	}
}

func TestNodeCommand(t *testing.T) {
	cases := []struct {
		cfg  NodeConfig
		want []string
	}{
		{NodeConfig{DevServer: true, Preset: "vite"}, []string{"npm", "run", "dev", "--", "--host", "0.0.0.0", "--port", "5173", "--strictPort"}},
		{NodeConfig{DevServer: true, Preset: "next"}, []string{"npm", "run", "dev", "--", "-H", "0.0.0.0", "-p", "3000"}},
		{NodeConfig{DevServer: true, Preset: "nuxt"}, []string{"npm", "run", "dev", "--", "--host", "0.0.0.0", "--port", "3000"}},
		// pnpm and Yarn hand a "--" on to the script, and Vite would ignore what follows.
		{NodeConfig{DevServer: true, Preset: "vite", PackageManager: "pnpm"}, []string{"pnpm", "run", "dev", "--host", "0.0.0.0", "--port", "5173", "--strictPort"}},
		{NodeConfig{DevServer: true, Preset: "next", PackageManager: "yarn"}, []string{"yarn", "dev", "-H", "0.0.0.0", "-p", "3000"}},
		{NodeConfig{DevServer: true, Preset: "generic", PackageManager: "yarn", Script: "start"}, []string{"yarn", "start"}},
	}
	for _, tc := range cases {
		if err := tc.cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		if got := tc.cfg.Command(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.cfg.Preset, got, tc.want)
		}
	}
}

func TestNodeWrappedCommand(t *testing.T) {
	c := NodeConfig{DevServer: true, Preset: "vite"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	got := c.WrappedCommand()
	want := append([]string{"sh", "-c", "rm -f " + WaitingFile + "; " + waitForPackageJSON + "; " + c.DepsGuard() + "; " + nodeDevEnv + "; " + supervise, "envoryx-dev"}, c.Command()...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The argv is passed as positional parameters, never spliced into the script.
	if !contains(got[2], waitForPackageJSON) || !contains(got[2], `"$@" &`) || contains(got[2], "--strictPort") {
		t.Fatalf("script must not embed the command: %q", got[2])
	}
	// A further guard (the planner's database wait) runs after the package.json guard and
	// before the command; an empty one changes nothing.
	if plain := c.WrappedCommand(""); !reflect.DeepEqual(plain, got) {
		t.Fatalf("empty guard changed the command: %q", plain)
	}
	guarded := c.WrappedCommand("until true; do :; done")
	if i, j := indexOf(guarded[2], waitForPackageJSON), indexOf(guarded[2], "until true"); i < 0 || j < i {
		t.Fatalf("guard order: %q", guarded[2])
	}
	if !contains(guarded[2], nodeDevEnv+"; until true; do :; done; "+supervise) {
		t.Fatalf("guards must be chained: %q", guarded[2])
	}
}

func TestNodeEnv(t *testing.T) {
	c := NodeConfig{DevServer: true, Preset: "next"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	// Exactly one entry, passed verbatim: Vite before 8.3 does not split the variable on
	// commas, so a joined list would match no Host header at all.
	got := c.Env(".test")
	want := []string{"HOST=0.0.0.0", "PORT=3000", "NITRO_HOST=0.0.0.0", "NITRO_PORT=3000", "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if none := c.Env(""); len(none) != 4 {
		t.Fatalf("no hosts must not set the allow-list: %q", none)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Production mode builds first and serves with NODE_ENV=production on the serve process
// only; script defaults follow the preset; the inspector adds a validated second port.
func TestNodeProductionAndInspector(t *testing.T) {
	c := NodeConfig{DevServer: true, Mode: "production", Preset: "next"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Script != "start" || c.BuildScript != "build" || !c.Production() {
		t.Fatalf("next production defaults: %+v", c)
	}
	want := []string{"sh", "-c", `NODE_ENV=production npm run build && NODE_ENV=production exec "$@"`, "envoryx-start", "npm", "run", "start", "--", "-H", "0.0.0.0", "-p", "3000"}
	if got := c.Command(); !reflect.DeepEqual(got, want) {
		t.Fatalf("next production command:\n got %q\nwant %q", got, want)
	}

	vite := NodeConfig{DevServer: true, Mode: "production", Preset: "vite", PackageManager: "yarn"}
	if err := vite.Normalize(); err != nil {
		t.Fatal(err)
	}
	if vite.Script != "preview" {
		t.Fatalf("vite production script = %q, want preview", vite.Script)
	}
	if got := vite.Command(); got[2] != `NODE_ENV=production yarn build && NODE_ENV=production exec "$@"` || got[4] != "yarn" || got[5] != "preview" || got[len(got)-1] != "--strictPort" {
		t.Fatalf("vite/yarn production command: %q", got)
	}

	// Nuxt's starter has no start script; production defaults to its preview server.
	nuxt := NodeConfig{DevServer: true, Mode: "production", Preset: "nuxt"}
	if err := nuxt.Normalize(); err != nil {
		t.Fatal(err)
	}
	if nuxt.Script != "preview" {
		t.Fatalf("nuxt production script = %q, want preview", nuxt.Script)
	}
	// nuxt preview takes no host/port flags; Nitro reads NITRO_HOST/NITRO_PORT from Env().
	if got := nuxt.Command(); !reflect.DeepEqual(got[4:], []string{"npm", "run", "preview"}) {
		t.Fatalf("nuxt production serve argv: %q", got[4:])
	}

	dev := NodeConfig{DevServer: true, Preset: "vite", BuildScript: "build"}
	if err := dev.Normalize(); err != nil {
		t.Fatal(err)
	}
	if dev.Mode != NodeModeDev || dev.BuildScript != "" || dev.Command()[0] != "npm" {
		t.Fatalf("dev mode must stay a plain command: %+v %q", dev, dev.Command())
	}
	if err := (&NodeConfig{DevServer: true, Mode: "staging"}).Normalize(); err == nil {
		t.Fatal("unknown mode must fail")
	}
	if err := (&NodeConfig{DevServer: true, Mode: "production", BuildScript: "build && rm -rf /"}).Normalize(); err == nil {
		t.Fatal("a build script that is not a script name must fail")
	}

	insp := NodeConfig{DevServer: true, Preset: "vite", Inspect: true}
	if err := insp.Normalize(); err != nil {
		t.Fatal(err)
	}
	if insp.InspectPort != DefaultInspectPort {
		t.Fatalf("inspect port default = %d", insp.InspectPort)
	}
	if err := (&NodeConfig{DevServer: true, Preset: "vite", Inspect: true, InspectPort: 5173}).Normalize(); err == nil {
		t.Fatal("inspector on the dev-server port must fail")
	}
	off := NodeConfig{DevServer: true, Preset: "vite", InspectPort: 9229, InspectHostPort: 20005}
	if err := off.Normalize(); err != nil {
		t.Fatal(err)
	}
	if off.InspectPort != 0 || off.InspectHostPort != 0 {
		t.Fatalf("inspector ports must be cleared when Inspect is off: %+v", off)
	}
}

// The dev server installs from the configured package manager's lockfile first: a
// repository with a stale package-lock.json next to its pnpm or Yarn lockfile must not be
// installed with npm.
func TestNodeDepsGuardPrefersPackageManager(t *testing.T) {
	for pm, first := range map[string]string{"npm": "package-lock.json", "pnpm": "pnpm-lock.yaml", "yarn": "yarn.lock"} {
		c := NodeConfig{DevServer: true, PackageManager: pm}
		if err := c.Normalize(); err != nil {
			t.Fatal(err)
		}
		locks := NodeLockfiles(c.PackageManager)
		if len(locks) != 3 || locks[0].File != first {
			t.Fatalf("%s: lockfile order %v", pm, locks)
		}
		g := c.DepsGuard()
		if !contains(g, "if [ -f "+first+" ]") {
			t.Fatalf("%s: the guard must test %s first: %s", pm, first, g)
		}
		for _, l := range locks[1:] {
			if !contains(g, "elif [ -f "+l.File+" ]") || indexOf(g, l.File) < indexOf(g, first) {
				t.Fatalf("%s: %s must come after %s: %s", pm, l.File, first, g)
			}
		}
		if out, err := exec.Command("sh", "-n", "-c", g).CombinedOutput(); err != nil {
			t.Fatalf("%s: guard is no valid shell: %v %s", pm, err, out)
		}
	}
}

// Yarn 1 installs with --frozen-lockfile, later versions with --immutable, which spares
// them the YN0050 deprecation.
func TestYarnFrozenInstallFlag(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo \"$YARN_VERSION\"; else echo \"$@\"; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "yarn"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]string{"1.22.22": "install --frozen-lockfile", "4.18.1": "install --immutable"} {
		cmd := exec.Command("sh", "-c", yarnFrozenInstall)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "YARN_VERSION="+version)
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("yarn %s: %q %v, want %q", version, out, err, want)
		}
	}
}

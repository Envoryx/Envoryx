package runtime

import (
	"reflect"
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
	want := append([]string{"sh", "-c", waitForPackageJSON, "envoryx-dev"}, c.Command()...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The argv is passed as positional parameters, never spliced into the script.
	if got[2] != waitForPackageJSON || !contains(got[2], `exec "$@"`) || contains(got[2], "npm") {
		t.Fatalf("script must not embed the command: %q", got[2])
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

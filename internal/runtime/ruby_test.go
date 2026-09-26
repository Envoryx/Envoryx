package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestRubyConfigDefaults(t *testing.T) {
	for _, tc := range []struct {
		preset string
		port   int
		want   int
		key    string
	}{
		{"", 0, 3000, "rails"},
		{"rails", 0, 3000, "rails"},
		{"Rack", 0, 9292, "rack"},
		{"rack", 4567, 4567, "rack"}, // an explicit port wins over the preset default
	} {
		c := RubyConfig{Server: true, Preset: tc.preset, Port: tc.port}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%q: %v", tc.preset, err)
		}
		if c.Preset != tc.key || c.Port != tc.want || c.Mode != RubyModeDev || c.DebugPort != 0 {
			t.Errorf("preset %q: %+v", tc.preset, c)
		}
	}
	tool := RubyConfig{Preset: "rack", Port: 9000, Debug: true}
	if err := tool.Normalize(); err != nil || tool != (RubyConfig{Debug: true, DebugPort: DefaultRdbgPort}) {
		t.Fatalf("a tooling container keeps only the debugger: %+v %v", tool, err)
	}
	for _, bad := range []RubyConfig{
		{Server: true, Preset: "hanami"},
		{Server: true, Mode: "staging"},
		{Server: true, Port: 80},
		{Server: true, Debug: true, DebugPort: 3000},
		{Debug: true, DebugPort: 70000},
	} {
		if err := bad.Normalize(); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", bad, err)
		}
	}
}

func TestRubyCommands(t *testing.T) {
	dev := RubyConfig{Server: true}
	_ = dev.Normalize()
	if cmd := dev.Command(); !slices.Equal(cmd, []string{"bin/rails", "server", "-b", "0.0.0.0", "-p", "3000", "-P", railsPidFile}) {
		t.Fatalf("rails dev: %q", cmd)
	}
	prod := RubyConfig{Server: true, Mode: "production"}
	_ = prod.Normalize()
	if cmd := prod.Command(); !slices.Equal(cmd, []string{"bundle", "exec", "puma", "-b", "tcp://0.0.0.0:3000"}) {
		t.Fatalf("rails production: %q", cmd)
	}
	rack := RubyConfig{Server: true, Preset: "rack", Debug: true}
	_ = rack.Normalize()
	cmd := rack.Command()
	if !slices.Equal(cmd[3:], []string{"envoryx-rdbg", "12345", "bundle", "exec", "puma", "-b", "tcp://0.0.0.0:9292"}) {
		t.Fatalf("rack under rdbg: %q", cmd)
	}

	// The Rails server waits for the application, installs the bundle and clears a stale
	// pid file; the database guard comes last.
	wrapped := dev.WrappedCommand("until db; do sleep 1; done")
	script := wrapped[2]
	for _, want := range []string{"[ -e bin/rails ]", "bundle check", "rm -f " + railsPidFile, "until db"} {
		if !strings.Contains(script, want) {
			t.Fatalf("guards lack %q: %s", want, script)
		}
	}
	if strings.Index(script, "bundle check") > strings.Index(script, "until db") {
		t.Fatalf("the bundle comes before the database wait: %s", script)
	}
	if w := prod.WrappedCommand(); strings.Contains(w[2], "rm -f") {
		t.Fatalf("Puma writes no pid file of its own: %s", w[2])
	}
	if w := rack.WrappedCommand(); !strings.Contains(w[2], "[ -e config.ru ]") {
		t.Fatalf("rack waits for config.ru: %s", w[2])
	}

	env := dev.Env(".test")
	for _, want := range []string{"PORT=3000", "HOST=0.0.0.0", "RAILS_ENV=development", "RACK_ENV=development", "RAILS_DEVELOPMENT_HOSTS=.test"} {
		if !slices.Contains(env, want) {
			t.Fatalf("dev env lacks %s: %v", want, env)
		}
	}
	env = prod.Env(".test")
	if !slices.Contains(env, "RAILS_ENV=production") || !slices.Contains(env, "RAILS_SERVE_STATIC_FILES=1") || slices.Contains(env, "RAILS_DEVELOPMENT_HOSTS=.test") {
		t.Fatalf("production env: %v", env)
	}
}

// TestRdbgScript runs the rdbg wrapper against stand-ins for rdbg and bundle: the bundle's
// rdbg when Gemfile.lock locks the debug gem, else the image's.
func TestRdbgScript(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	for _, name := range []string{"rdbg", "bundle"} {
		stub := "#!/bin/sh\nprintf '" + name + "'; printf ' %s' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func() string {
		cmd := exec.Command("sh", "-c", rdbgScript, "envoryx-rdbg", "12345", "bin/rails", "server", "-b", "0.0.0.0")
		cmd.Dir, cmd.Env = dir, []string{"PATH=" + bin + ":/usr/bin:/bin"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if out := run(); out != "rdbg --open --host=0.0.0.0 --port=12345 --nonstop -c -- bin/rails server -b 0.0.0.0" {
		t.Fatalf("without a locked debug gem: %q", out)
	}
	lock := "GEM\n  remote: https://rubygems.org/\n  specs:\n    debug (1.11.1)\n      irb (~> 1.10)\n"
	if err := os.WriteFile(filepath.Join(dir, "Gemfile.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(); !strings.HasPrefix(out, "bundle exec rdbg --open") {
		t.Fatalf("with the debug gem in the bundle: %q", out)
	}
}

package runtime

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestGoConfigDefaults(t *testing.T) {
	c := GoConfig{Server: true, Package: "cmd/server/"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Mode != GoModeDev || c.Package != "./cmd/server" || c.Port != DefaultGoPort || c.Debug || c.DebugPort != 0 {
		t.Fatalf("defaults: %+v", c)
	}
	tool := GoConfig{Package: "./x", Port: 9000, Debug: true}
	if err := tool.Normalize(); err != nil || tool.Package != "" || tool.Port != 0 || tool.DebugPort != DefaultDelvePort {
		t.Fatalf("a tooling container keeps only the debugger: %+v %v", tool, err)
	}
	for _, bad := range []GoConfig{
		{Server: true, Package: "../outside"},
		{Server: true, Package: "./a/../b"},
		{Server: true, Package: "./a b"},
		{Server: true, Package: "./x;rm -rf"},
		{Server: true, Port: 80},
		{Server: true, Mode: "turbo"},
		{Server: true, Debug: true, DebugPort: 8080},
	} {
		if err := bad.Normalize(); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", bad, err)
		}
	}
}

func TestGoCommands(t *testing.T) {
	dev := GoConfig{Server: true, Package: "./cmd/server"}
	_ = dev.Normalize()
	cmd := dev.Command()
	if cmd[0] != "sh" || !strings.Contains(cmd[2], ".air.toml") || !slices.Contains(cmd, "go build -o /tmp/envoryx-go/app ./cmd/server") || slices.Contains(cmd, "--build.full_bin") {
		t.Fatalf("dev command: %q", cmd)
	}
	dev.Debug = true
	_ = dev.Normalize()
	cmd = dev.Command()
	if !slices.Contains(cmd, "go build '-gcflags=all=-N -l' -o /tmp/envoryx-go/app ./cmd/server") || !slices.Contains(cmd, "--build.full_bin") {
		t.Fatalf("dev debug command: %q", cmd)
	}
	prod := GoConfig{Server: true, Mode: "production", Debug: true, DebugPort: 4000}
	_ = prod.Normalize()
	cmd = prod.Command()
	if !slices.Equal(cmd[3:], []string{"envoryx-go-build", "debug", ".", "4000"}) {
		t.Fatalf("production command: %q", cmd)
	}
	wrapped := prod.WrappedCommand("until db; do sleep 1; done")
	if !strings.Contains(wrapped[2], "go.mod") || !strings.Contains(wrapped[2], "until db") {
		t.Fatalf("guards: %q", wrapped[2])
	}
	if env := prod.Env(); !slices.Equal(env, []string{"HOST=0.0.0.0", "PORT=8080", "GIN_MODE=release"}) {
		t.Fatalf("env: %v", env)
	}
}

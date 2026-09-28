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

func TestDotnetConfigDefaults(t *testing.T) {
	for _, tc := range []struct {
		preset string
		port   int
		want   int
		key    string
	}{
		{"", 0, 8080, "aspnetcore"},
		{"DLL", 0, 8080, "dll"},
		{"aspnetcore", 5000, 5000, "aspnetcore"}, // an explicit port wins over the preset default
	} {
		c := DotnetConfig{Server: true, Preset: tc.preset, Port: tc.port}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%q: %v", tc.preset, err)
		}
		if c.Preset != tc.key || c.Port != tc.want || c.Mode != DotnetModeDev {
			t.Errorf("preset %q: %+v", tc.preset, c)
		}
	}
	tool := DotnetConfig{Preset: "dll", Port: 9000, Project: "App.csproj"}
	if err := tool.Normalize(); err != nil || tool != (DotnetConfig{}) {
		t.Fatalf("a tooling container keeps nothing: %+v %v", tool, err)
	}
	// The DLL only belongs to the "dll" preset.
	web := DotnetConfig{Server: true, DLL: "bin/envoryx-publish/App.dll", Project: "./src/Shop/Shop.csproj"}
	if err := web.Normalize(); err != nil || web.DLL != "" || web.Project != "src/Shop/Shop.csproj" {
		t.Fatalf("aspnetcore drops the DLL and cleans the project: %+v %v", web, err)
	}
	dll := DotnetConfig{Server: true, Preset: "dll", DLL: "./bin/envoryx-publish/Worker.dll", Project: "Worker.fsproj"}
	if err := dll.Normalize(); err != nil || dll.DLL != "bin/envoryx-publish/Worker.dll" {
		t.Fatalf("dll keeps a cleaned path: %+v %v", dll, err)
	}
	for _, bad := range []DotnetConfig{
		{Server: true, Preset: "blazor"},
		{Server: true, Mode: "staging"},
		{Server: true, Port: 80},
		{Server: true, Project: "../other/App.csproj"},
		{Server: true, Project: "/src/App.csproj"},
		{Server: true, Project: "App.sln"},
		{Server: true, Project: "src/$(id).csproj"},
		{Server: true, Preset: "dll", DLL: "bin/App.exe"},
		{Server: true, Preset: "dll", DLL: "bin/../App.dll"},
	} {
		if err := bad.Normalize(); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", bad, err)
		}
	}
}

func TestDotnetCommandsAndEnv(t *testing.T) {
	dev := DotnetConfig{Server: true}
	_ = dev.Normalize()
	if cmd := dev.Command(); !slices.Equal(cmd[3:], []string{"envoryx-dotnet", "aspnetcore", "dev", "", "", "8080"}) {
		t.Fatalf("aspnetcore dev: %q", cmd)
	}
	// The "dll" preset has no dev mode of its own: it always publishes and runs the DLL.
	dll := DotnetConfig{Server: true, Preset: "dll", Project: "Worker.csproj", Port: 9000}
	_ = dll.Normalize()
	if cmd := dll.Command(); !slices.Equal(cmd[3:], []string{"envoryx-dotnet", "dll", "production", "Worker.csproj", "", "9000"}) {
		t.Fatalf("dll: %q", cmd)
	}
	wrapped := dev.WrappedCommand("until db; do sleep 1; done")
	for _, want := range []string{"*.csproj", "*.fsproj", "until db"} {
		if !strings.Contains(wrapped[2], want) {
			t.Fatalf("guards lack %q: %s", want, wrapped[2])
		}
	}
	// dotnet watch gets the address from --urls; the ports variable stays empty.
	if env := dev.Env(); !slices.Contains(env, "ASPNETCORE_HTTP_PORTS=") || !slices.Contains(env, "ASPNETCORE_ENVIRONMENT=Development") || !slices.Contains(env, "PORT=8080") {
		t.Fatalf("dev env: %v", env)
	}
	prod := DotnetConfig{Server: true, Mode: "production"}
	_ = prod.Normalize()
	if env := prod.Env(); !slices.Contains(env, "ASPNETCORE_HTTP_PORTS=8080") || !slices.Contains(env, "ASPNETCORE_ENVIRONMENT=Production") || !slices.Contains(env, "DOTNET_ENVIRONMENT=Production") {
		t.Fatalf("production env: %v", env)
	}
	// The "dll" preset runs the published application in dev mode too, on the ports.
	if env := dll.Env(); !slices.Contains(env, "ASPNETCORE_HTTP_PORTS=9000") {
		t.Fatalf("dll env: %v", env)
	}
}

// TestDotnetServeScript runs the start script against a stand-in for dotnet that prints
// what it was called with and, for publish, leaves an application in the output
// directory the way the real one does.
func TestDotnetServeScript(t *testing.T) {
	bin := t.TempDir()
	stub := `#!/bin/sh
printf 'dotnet'; printf ' %s' "$@"; printf '\n'
if [ "$1" = publish ]; then
  out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
  mkdir -p "$out"; : > "$out/Shop.runtimeconfig.json"; : > "$out/Shop.dll"
  [ -n "$DOTNET_STUB_TWO" ] && : > "$out/Other.runtimeconfig.json"
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "dotnet"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	project := func(files map[string]string) string {
		dir := t.TempDir()
		for f, content := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	run := func(dir string, env []string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{"-c", dotnetServeScript, "envoryx-dotnet"}, args...)...)
		cmd.Dir, cmd.Env = dir, append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, env...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	web := `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`
	lib := `<Project Sdk="Microsoft.NET.Sdk"></Project>`

	t.Run("dev watches the one project at the top", func(t *testing.T) {
		out, err := run(project(map[string]string{"Shop.csproj": web}), nil, "aspnetcore", "dev", "", "", "8080")
		if want := "dotnet watch --non-interactive --project ./Shop.csproj run -- --urls http://0.0.0.0:8080"; err != nil || out != want {
			t.Fatalf("got %q (%v), want %q", out, err, want)
		}
	})
	t.Run("dev finds the web project below", func(t *testing.T) {
		dir := project(map[string]string{
			"Shop.sln": "", "src/Shop/Shop.csproj": web, "src/Shop.Core/Shop.Core.csproj": lib,
			"tests/Shop.Tests/Shop.Tests.csproj": lib, "src/Shop/bin/Debug/Copy.csproj": web,
		})
		out, err := run(dir, nil, "aspnetcore", "dev", "", "", "5000")
		if want := "dotnet watch --non-interactive --project ./src/Shop/Shop.csproj run -- --urls http://0.0.0.0:5000"; err != nil || out != want {
			t.Fatalf("got %q (%v), want %q", out, err, want)
		}
	})
	t.Run("two web projects need a choice", func(t *testing.T) {
		dir := project(map[string]string{"src/A/A.csproj": web, "src/B/B.csproj": web})
		out, err := run(dir, nil, "aspnetcore", "dev", "", "", "8080")
		if err == nil || !strings.Contains(out, "found 2 web or worker projects") {
			t.Fatalf("got %q (%v)", out, err)
		}
		out, err = run(dir, nil, "aspnetcore", "dev", "src/B/B.csproj", "", "8080")
		if err != nil || !strings.Contains(out, "--project src/B/B.csproj") {
			t.Fatalf("a configured project wins: %q (%v)", out, err)
		}
	})
	t.Run("production publishes and runs the application", func(t *testing.T) {
		dir := project(map[string]string{"Shop.csproj": web})
		out, err := run(dir, nil, "aspnetcore", "production", "", "", "8080")
		want := "dotnet publish ./Shop.csproj -c Release -o " + DotnetPublishDir + " --nologo -p:UseSharedCompilation=false\ndotnet Shop.dll"
		if err != nil || out != want {
			t.Fatalf("got %q (%v), want %q", out, err, want)
		}
	})
	t.Run("dll preset runs the configured DLL", func(t *testing.T) {
		dir := project(map[string]string{"Worker.csproj": lib, "tools/Tool.dll": ""})
		out, err := run(dir, nil, "dll", "production", "", "tools/Tool.dll", "8080")
		if want := "dotnet publish ./Worker.csproj -c Release -o " + DotnetPublishDir + " --nologo -p:UseSharedCompilation=false\ndotnet Tool.dll"; err != nil || out != want {
			t.Fatalf("got %q (%v)", out, err)
		}
	})
	t.Run("two applications need a DLL", func(t *testing.T) {
		out, err := run(project(map[string]string{"Worker.csproj": lib}), []string{"DOTNET_STUB_TWO=1"}, "dll", "production", "", "", "8080")
		if err == nil || !strings.Contains(out, "set the DLL") {
			t.Fatalf("got %q (%v)", out, err)
		}
	})
}

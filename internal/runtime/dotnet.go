package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// DotnetConfig configures the .NET service. Without Server the container idles as a
// tooling container (dotnet, dotnet-ef, netcoredbg); with it the container runs the
// preset's application as its main process. A project without PHP is then served by it:
// the embedded proxy routes the project's primary hostname to the .NET container and the
// web container's port stays unpublished. There is no debug port: IDEs start netcoredbg
// (VS Code) or their own debugger (Rider) inside the container over SSH and attach to the
// running process.
type DotnetConfig struct {
	Server bool `json:"server"`
	// Mode is "dev" (dotnet watch with hot reload, default) or "production": the project
	// is published once and the published DLL runs.
	Mode string `json:"mode,omitempty"`
	// Preset selects the server command: "aspnetcore" (dotnet watch in dev) or "dll"
	// (publish, then dotnet <dll>; worker services, console hosts …).
	Preset string `json:"preset,omitempty"`
	// Project is the project file to run, relative to the project. Empty means the one
	// project file at the top, else the one web or worker project below it.
	Project string `json:"project,omitempty"`
	// DLL is the DLL the "dll" preset runs, relative to the project. Empty means the
	// application the publish produced.
	DLL string `json:"dll,omitempty"`
	// Port the server listens on inside the container (default 8080).
	Port int `json:"port,omitempty"`
	// HostPort publishes the server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
}

// .NET run modes and defaults.
const (
	DotnetModeDev        = "dev"
	DotnetModeProduction = "production"
	DefaultDotnetPort    = 8080
	// DotnetPublishDir is where the production start publishes the project, relative to
	// the project directory. bin/ is in every .NET .gitignore.
	DotnetPublishDir = "bin/envoryx-publish"
	// NetcoredbgPath is where the image installs the debugger IDEs start over SSH.
	NetcoredbgPath = "/usr/local/bin/netcoredbg"
)

// DotnetPreset describes a supported application server and its defaults.
type DotnetPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// DotnetPresets lists the supported presets in UI order.
var DotnetPresets = []DotnetPreset{
	{Key: "aspnetcore", Label: "ASP.NET Core (Web API, MVC, Razor Pages, Blazor)", Port: 8080},
	{Key: "dll", Label: "Other (publish, then dotnet <dll>: worker services, console hosts …)", Port: 8080},
}

// DotnetPresetByKey looks up a preset.
func DotnetPresetByKey(key string) (DotnetPreset, bool) {
	for _, p := range DotnetPresets {
		if p.Key == key {
			return p, true
		}
	}
	return DotnetPreset{}, false
}

// dotnetPathRe accepts a relative path without "..": the value goes into the start script
// as an argument, so it stays plain.
var dotnetPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

func validDotnetPath(s string, exts ...string) bool {
	if len(s) > 200 || !dotnetPathRe.MatchString(s) || strings.Contains(s, "..") {
		return false
	}
	for _, ext := range exts {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

// ValidDotnetProject reports whether s is a relative path to a C#, F# or Visual Basic
// project file.
func ValidDotnetProject(s string) bool { return validDotnetPath(s, ".csproj", ".fsproj", ".vbproj") }

// ValidDotnetDLL reports whether s is a relative path to a DLL.
func ValidDotnetDLL(s string) bool { return validDotnetPath(s, ".dll") }

// Normalize validates the configuration and fills defaults.
func (c *DotnetConfig) Normalize() error {
	if !c.Server {
		*c = DotnetConfig{}
		return nil
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", DotnetModeDev:
		c.Mode = DotnetModeDev
	case DotnetModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "aspnetcore"
	}
	preset, ok := DotnetPresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown .NET server preset %q", validate.ErrInvalid, c.Preset)
	}
	c.Project = strings.TrimPrefix(strings.TrimSpace(c.Project), "./")
	if c.Project != "" && !ValidDotnetProject(c.Project) {
		return fmt.Errorf("%w: invalid project %q (use a relative path such as src/Shop/Shop.csproj)", validate.ErrInvalid, c.Project)
	}
	c.DLL = strings.TrimPrefix(strings.TrimSpace(c.DLL), "./")
	if c.Preset != "dll" {
		c.DLL = ""
	}
	if c.DLL != "" && !ValidDotnetDLL(c.DLL) {
		return fmt.Errorf("%w: invalid DLL %q (use a relative path such as %s/Worker.dll)", validate.ErrInvalid, c.DLL, DotnetPublishDir)
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the project is published once and the DLL runs.
func (c DotnetConfig) Production() bool { return c.Mode == DotnetModeProduction }

// dotnetFindProject sets $proj when it is empty: the one project file at the top of the
// project, else the one ASP.NET Core or worker project up to four levels down (bin/, obj/,
// node_modules/ and hidden directories skipped). Anything else is ambiguous and stops
// with a message that says where to choose.
const dotnetFindProject = `if [ -z "$proj" ]; then
  top=$(find . -maxdepth 1 -type f \( -name '*.csproj' -o -name '*.fsproj' -o -name '*.vbproj' \))
  if [ "$(printf '%s\n' "$top" | grep -c .)" = 1 ]; then proj=$top
  else
    apps=$(find . -maxdepth 4 \( -name bin -o -name obj -o -name node_modules -o \( -name '.*' ! -name . \) \) -prune -o -type f \( -name '*.csproj' -o -name '*.fsproj' -o -name '*.vbproj' \) -exec grep -lE 'Sdk="Microsoft\.NET\.Sdk\.(Web|Worker)"' {} + 2>/dev/null || true)
    n=$(printf '%s\n' "$apps" | grep -c . || true)
    if [ "$n" != 1 ]; then echo "envoryx: found $n web or worker projects and not exactly one project file at the top - choose the project in the .NET settings" >&2; exit 1; fi
    proj=$apps
  fi
fi`

// dotnetServeScript starts the server: $1 is the preset, $2 the mode, $3 the project file
// (may be empty), $4 the DLL of the "dll" preset (may be empty) and $5 the port. Dev mode
// runs dotnet watch, which applies code changes with hot reload and restarts the
// application when it can't; --urls wins over the applicationUrl of the project's launch
// profile, which only listens on localhost. Production and the "dll" preset publish once,
// without the compiler server that would otherwise linger next to the application, and
// exec dotnet from the output directory (the content root, where wwwroot and the
// appsettings files are), so the application is the container's main process and gets
// the stop signal directly.
const dotnetServeScript = `set -e
preset=$1 mode=$2 proj=$3 dll=$4 port=$5
` + dotnetFindProject + `
if [ "$mode" = dev ] && [ "$preset" = aspnetcore ]; then
  exec dotnet watch --non-interactive --project "$proj" run -- --urls "http://0.0.0.0:$port"
fi
out=` + DotnetPublishDir + `
rm -rf "$out"
dotnet publish "$proj" -c Release -o "$out" --nologo -p:UseSharedCompilation=false
if [ -z "$dll" ]; then
  set -- "$out"/*.runtimeconfig.json
  if [ $# -ne 1 ] || [ ! -f "$1" ]; then echo "envoryx: the publish left no single application in $out - set the DLL in the .NET settings" >&2; exit 1; fi
  dll=${1%.runtimeconfig.json}.dll
fi
if [ ! -f "$dll" ]; then echo "envoryx: $dll not found after the publish" >&2; exit 1; fi
cd "$(dirname "$dll")"
exec dotnet "$(basename "$dll")"`

// Command returns the argv of the container's main process. Every value comes from
// Normalize and is passed as an argument, never pasted into the script.
func (c DotnetConfig) Command() []string {
	mode := c.Mode
	if c.Preset == "dll" {
		mode = DotnetModeProduction // there is no dev mode to run
	}
	return []string{"sh", "-c", dotnetServeScript, "envoryx-dotnet", c.Preset, mode, c.Project, c.DLL, strconv.Itoa(c.Port)}
}

// entryGuard blocks until the project has a project file: a blank project would send the
// build into a crash-loop.
func (c DotnetConfig) entryGuard() string {
	return `until [ -n "$(find . -maxdepth 4 \( -name bin -o -name obj -o -name node_modules \) -prune -o -type f \( -name '*.csproj' -o -name '*.fsproj' -o -name '*.vbproj' \) -print 2>/dev/null | head -n 1)" ]; do echo 'envoryx: waiting for a .csproj in /var/www/html - scaffold with a .NET template, clone a repository or use the .NET terminal'; sleep 5; done`
}

// WrappedCommand returns Command() behind the project-file guard, for containers whose
// server is the project's application. Further guards (the database wait the planner
// builds) run after it.
func (c DotnetConfig) WrappedCommand(guards ...string) []string {
	return Guarded(c.Command(), "envoryx-serve", append([]string{c.entryGuard()}, guards...)...)
}

// Env returns the variables that tell the application where to listen and in which
// environment it runs. ASPNETCORE_HTTP_PORTS binds every interface; under dotnet watch
// --urls does that and the variable is emptied, or ASP.NET Core warns on every start that
// the URLs override the ports (the SDK image sets 8080). The forwarded headers switch
// makes ASP.NET Core trust the proxy's X-Forwarded-* headers, so redirects and generated
// links keep the project URL's scheme and host.
func (c DotnetConfig) Env() []string {
	port := strconv.Itoa(c.Port)
	env := "Development"
	if c.Production() {
		env = "Production"
	}
	httpPorts := port
	if c.Preset == "aspnetcore" && !c.Production() {
		httpPorts = ""
	}
	return []string{
		"HOST=0.0.0.0", "PORT=" + port,
		"ASPNETCORE_HTTP_PORTS=" + httpPorts,
		"ASPNETCORE_ENVIRONMENT=" + env, "DOTNET_ENVIRONMENT=" + env,
		"ASPNETCORE_FORWARDEDHEADERS_ENABLED=true",
	}
}

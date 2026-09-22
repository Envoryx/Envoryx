package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// NodeConfig configures the Node.js service. Without DevServer the container idles as a
// tooling container; with it the container runs the package.json script as its main
// process and the embedded proxy routes <slug>-dev.<base> to it. A project may consist of
// web + node only; the embedded proxy then routes the project's primary hostname to the
// dev server.
type NodeConfig struct {
	DevServer bool `json:"devServer"`
	// Mode is "dev" (the script is a dev server with HMR, default) or "production": every
	// container start runs BuildScript first, then Script as the main process with
	// NODE_ENV=production – a production-like run of Next.js/Nuxt/Vite preview.
	Mode string `json:"mode,omitempty"`
	// PackageManager is npm, pnpm or yarn.
	PackageManager string `json:"packageManager,omitempty"`
	// Script is the package.json script to run (default "dev"; "start" in production mode,
	// "preview" for the Vite preset).
	Script string `json:"script,omitempty"`
	// BuildScript is the package.json script run before Script in production mode
	// (default "build").
	BuildScript string `json:"buildScript,omitempty"`
	// Port the dev server listens on inside the container (default: the preset's port).
	Port int `json:"port,omitempty"`
	// Preset selects how host/port are passed: "vite", "next", "nuxt" or "generic" (env only).
	Preset string `json:"preset,omitempty"`
	// HostPort publishes the dev server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
	// Inspect publishes the Node.js inspector port so an IDE can attach a debugger. The
	// container only publishes InspectPort; the script itself has to start the inspector
	// (--inspect=0.0.0.0:<port>) – set through NODE_OPTIONS on the whole container it would
	// attach to the package manager's own node process instead of the app.
	Inspect bool `json:"inspect,omitempty"`
	// InspectPort is the inspector port inside the container (default 9229).
	InspectPort int `json:"inspectPort,omitempty"`
	// InspectHostPort publishes the inspector on the Docker host (assigned by Envoryx).
	InspectHostPort int `json:"inspectHostPort,omitempty"`
}

// Node run modes.
const (
	NodeModeDev        = "dev"
	NodeModeProduction = "production"
	// DefaultInspectPort is Node's default inspector port.
	DefaultInspectPort = 9229
)

var scriptRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)

// NodePreset describes a supported dev-server framework and its default port.
type NodePreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// NodePresets lists the supported dev-server presets in UI order.
var NodePresets = []NodePreset{
	{Key: "vite", Label: "Vite (Vue, React, Svelte, Laravel…)", Port: 5173},
	{Key: "next", Label: "Next.js", Port: 3000},
	{Key: "nuxt", Label: "Nuxt", Port: 3000},
	{Key: "generic", Label: "Other (HOST/PORT env only)", Port: 5173},
}

// NodePresetByKey looks up a preset.
func NodePresetByKey(key string) (NodePreset, bool) {
	for _, p := range NodePresets {
		if p.Key == key {
			return p, true
		}
	}
	return NodePreset{}, false
}

// waitForPackageJSON guards the dev-server command when the Node container is the
// project's application: a blank project has no package.json yet and "npm run dev" would
// crash-loop. Nothing is interpolated into it (see Guarded).
const waitForPackageJSON = `until [ -f package.json ]; do echo 'envoryx: waiting for package.json in /var/www/html - scaffold with a Node template, clone a repository or use the Node terminal'; sleep 5; done`

// Normalize validates the configuration and fills defaults.
func (c *NodeConfig) Normalize() error {
	if !c.DevServer {
		*c = NodeConfig{}
		return nil
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", NodeModeDev:
		c.Mode = NodeModeDev
	case NodeModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.PackageManager = strings.ToLower(strings.TrimSpace(c.PackageManager))
	if c.PackageManager == "" {
		c.PackageManager = "npm"
	}
	switch c.PackageManager {
	case "npm", "pnpm", "yarn":
	default:
		return fmt.Errorf("%w: package manager must be npm, pnpm or yarn", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "vite"
	}
	preset, ok := NodePresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown dev server preset %q", validate.ErrInvalid, c.Preset)
	}
	c.Script = strings.TrimSpace(c.Script)
	if c.Script == "" {
		c.Script = "dev"
		if c.Mode == NodeModeProduction {
			c.Script = "start"
			if c.Preset == "vite" {
				c.Script = "preview" // vite has no server of its own: "vite preview" serves the build
			}
		}
	}
	if !scriptRe.MatchString(c.Script) {
		return fmt.Errorf("%w: invalid script name %q", validate.ErrInvalid, c.Script)
	}
	c.BuildScript = strings.TrimSpace(c.BuildScript)
	if c.Mode != NodeModeProduction {
		c.BuildScript = ""
	} else {
		if c.BuildScript == "" {
			c.BuildScript = "build"
		}
		if !scriptRe.MatchString(c.BuildScript) {
			return fmt.Errorf("%w: invalid build script name %q", validate.ErrInvalid, c.BuildScript)
		}
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: dev server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if !c.Inspect {
		c.InspectPort, c.InspectHostPort = 0, 0
		return nil
	}
	if c.InspectPort == 0 {
		c.InspectPort = DefaultInspectPort
	}
	if c.InspectPort < 1024 || c.InspectPort > 65535 {
		return fmt.Errorf("%w: inspector port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if c.InspectPort == c.Port {
		return fmt.Errorf("%w: inspector port must differ from the dev server port", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the container builds and then serves the app.
func (c NodeConfig) Production() bool { return c.Mode == NodeModeProduction }

// runScript returns the argv that runs a package.json script with the package manager.
func (c NodeConfig) runScript(script string) []string {
	if c.PackageManager == "yarn" {
		return []string{"yarn", script}
	}
	return []string{c.PackageManager, "run", script}
}

// serveCommand returns the argv of the process that serves the app: the script with the
// preset's host/port flags. In production mode "nuxt preview" takes no flags – Nitro reads
// NITRO_HOST/NITRO_PORT from Env() – while "vite preview" and "next start" accept the
// same flags as their dev servers.
func (c NodeConfig) serveCommand() []string {
	cmd := c.runScript(c.Script)
	port := strconv.Itoa(c.Port)
	switch c.Preset {
	case "vite":
		cmd = append(cmd, "--", "--host", "0.0.0.0", "--port", port, "--strictPort")
	case "next":
		cmd = append(cmd, "--", "-H", "0.0.0.0", "-p", port)
	case "nuxt":
		if !c.Production() {
			cmd = append(cmd, "--", "--host", "0.0.0.0", "--port", port)
		}
	}
	return cmd
}

// Command returns the argv of the container's main process. In production mode the build
// script runs first on every start and the serve process gets NODE_ENV=production – only
// that process, so "npm install" from the terminal still installs devDependencies. Script
// names are validated against scriptRe, so interpolating them into the shell line is safe;
// the serve argv is passed through "$@" untouched.
func (c NodeConfig) Command() []string {
	serve := c.serveCommand()
	if !c.Production() {
		return serve
	}
	build := strings.Join(c.runScript(c.BuildScript), " ")
	script := build + ` && NODE_ENV=production exec "$@"`
	return append([]string{"sh", "-c", script, "envoryx-start"}, serve...)
}

// WrappedCommand returns Command() behind the package.json wait guard, for containers
// whose dev server is the project's application. Further guards (the database wait the
// planner builds) run after it.
func (c NodeConfig) WrappedCommand(guards ...string) []string {
	return Guarded(c.Command(), "envoryx-dev", append([]string{waitForPackageJSON}, guards...)...)
}

// Env returns the variables that make common dev servers listen on all interfaces and
// accept the proxied host name. allowedHost goes into Vite's allow-list verbatim: Vite
// before 8.3 appends the raw variable as a single entry (no splitting on commas), so exactly
// one value is passed – a leading dot makes it a suffix match for every name under that
// domain, which covers <slug>.<base>, <slug>-dev.<base> and extra domains under the base.
func (c NodeConfig) Env(allowedHost string) []string {
	port := strconv.Itoa(c.Port)
	env := []string{"HOST=0.0.0.0", "PORT=" + port, "NITRO_HOST=0.0.0.0", "NITRO_PORT=" + port}
	if allowedHost != "" {
		// Vite ≥ 6 rejects unknown Host headers unless allowed.
		env = append(env, "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS="+allowedHost)
	}
	return env
}

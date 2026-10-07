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
	// NODE_ENV=production - a production-like run of Next.js/Nuxt/Vite preview.
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
	// container only publishes InspectPort; the script has to start the inspector itself
	// (--inspect=0.0.0.0:<port>). Set through NODE_OPTIONS for the whole container, it
	// would attach to the package manager's own node process instead of the app.
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

// waitForPackageJSON guards the dev-server command: a blank project has no package.json
// yet and "npm run dev" would crash-loop, and a crash-looping container locks the user out
// of the terminal and of the actions that would fix it. Nothing is interpolated into the
// guards (see Guarded).
const waitForPackageJSON = `until [ -f package.json ]; do echo 'envoryx: waiting for package.json in /var/www/html - scaffold with a Node template, clone a repository or use the Node terminal'; sleep 5; done`

// nodeDepsMissing is true when package.json declares dependencies and nothing has
// installed them yet (no node_modules, no Yarn Plug'n'Play loader). A package.json without
// dependencies never gets a node_modules, so it must not be waited for.
const nodeDepsMissing = `[ ! -d node_modules ] && [ ! -f .pnp.cjs ] && node -e 'const p=require("./package.json");process.exit(Object.keys({...p.dependencies,...p.devDependencies}).length?0:1)' 2>/dev/null`

// nodeDepsWait blocks until an install has finished. It looks for the files npm, pnpm and
// Yarn write at the end of an install rather than for node_modules itself, which appears
// as soon as an install starts.
const nodeDepsWait = `until [ -f node_modules/.package-lock.json ] || [ -f node_modules/.modules.yaml ] || [ -f node_modules/.yarn-integrity ] || [ -f node_modules/.yarn-state.yml ] || [ -f .pnp.cjs ]; do echo 'envoryx: waiting for node_modules - run "npm install" (or pnpm / yarn install) from Actions'; sleep 5; done`

// NodeDepsGuard installs the dependencies of a fresh checkout before the dev server starts,
// like the Ruby containers run bundle install. It only installs from a lockfile and in
// frozen mode, so it reproduces what the repository pins and never writes to the user's
// files; without a lockfile, or when the install fails, it waits for an install from
// Actions instead of letting the server crash-loop.
const NodeDepsGuard = `if ` + nodeDepsMissing + `; then ` +
	`if [ -f package-lock.json ]; then echo 'envoryx: no node_modules - running "npm ci"'; npm ci; ` +
	`elif [ -f pnpm-lock.yaml ]; then echo 'envoryx: no node_modules - running "pnpm install --frozen-lockfile"'; pnpm install --frozen-lockfile; ` +
	`elif [ -f yarn.lock ]; then echo 'envoryx: no node_modules - running "yarn install --frozen-lockfile"'; yarn install --frozen-lockfile; ` +
	`else false; fi || ` + nodeDepsWait + `; fi`

// NodeDepsWaitGuard only waits for the dependencies. The Node workers use it, so they
// don't install into node_modules at the same time as the dev server.
const NodeDepsWaitGuard = `if ` + nodeDepsMissing + `; then ` + nodeDepsWait + `; fi`

// nodeDevEnv gives the dev server NODE_ENV=development unless the project sets its own.
// Only the dev server: set on the container it reaches every action and terminal command,
// and "npm run build" then ships development bundles (React's jsxDEV, a dev-mode
// process.env.NODE_ENV) or fails outright (next build).
const nodeDevEnv = `export NODE_ENV="${NODE_ENV:-development}"`

// Inherit fills the fields an update leaves empty from the stored configuration, so a
// call that only flips DevServer (API, CLI) keeps the preset, script and port. Script,
// build script and port depend on the preset and mode and are only taken over while the
// update keeps both.
func (c *NodeConfig) Inherit(old NodeConfig) {
	if strings.TrimSpace(c.PackageManager) == "" {
		c.PackageManager = old.PackageManager
	}
	if c.InspectPort == 0 {
		c.InspectPort = old.InspectPort
	}
	if p := strings.ToLower(strings.TrimSpace(c.Preset)); p != "" && p != old.Preset {
		return
	}
	c.Preset = old.Preset
	if m := strings.ToLower(strings.TrimSpace(c.Mode)); m != "" && m != old.Mode {
		return
	}
	c.Mode = old.Mode
	if strings.TrimSpace(c.Script) == "" {
		c.Script = old.Script
	}
	if strings.TrimSpace(c.BuildScript) == "" {
		c.BuildScript = old.BuildScript
	}
	if c.Port == 0 {
		c.Port = old.Port
	}
}

// Normalize validates the configuration and fills defaults. The server settings are kept
// (and checked) while DevServer is off, so turning it back on restores them.
func (c *NodeConfig) Normalize() error {
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
			// Vite has no server of its own ("vite preview" serves the build), and Nuxt's
			// starter has no start script: "nuxt preview" runs the built .output.
			if c.Preset == "vite" || c.Preset == "nuxt" {
				c.Script = "preview"
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
// preset's host/port flags. In production mode "nuxt preview" takes no flags - Nitro reads
// NITRO_HOST/NITRO_PORT from Env() - while "vite preview" and "next start" accept the
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
// script runs first on every start, and the build and the serve process get
// NODE_ENV=production - only those two, so "npm install" from the terminal still installs
// devDependencies. Script names are validated against scriptRe, so interpolating them into
// the shell line is safe; the serve argv is passed through "$@" untouched.
func (c NodeConfig) Command() []string {
	serve := c.serveCommand()
	if !c.Production() {
		return serve
	}
	build := strings.Join(c.runScript(c.BuildScript), " ")
	script := "NODE_ENV=production " + build + ` && NODE_ENV=production exec "$@"`
	return append([]string{"sh", "-c", script, "envoryx-start"}, serve...)
}

// WrappedCommand returns Command() behind the guards that keep a dev server from
// crash-looping - it waits for package.json and installs or waits for the dependencies -
// and, in dev mode, with NODE_ENV=development. Further guards (the database wait the
// planner builds) run after them.
func (c NodeConfig) WrappedCommand(guards ...string) []string {
	own := []string{waitForPackageJSON, NodeDepsGuard}
	if !c.Production() {
		own = append(own, nodeDevEnv)
	}
	return Guarded(c.Command(), "envoryx-dev", append(own, guards...)...)
}

// WorkerEnv is the NODE_ENV of the Node workers and cron jobs: they follow the dev
// server's mode, so a project that serves a production build doesn't run its queue
// consumers in development.
func (c NodeConfig) WorkerEnv() string {
	if c.DevServer && c.Production() {
		return "NODE_ENV=production"
	}
	return "NODE_ENV=development"
}

// Env returns the variables that make common dev servers listen on all interfaces and
// accept the proxied host name. allowedHost goes into Vite's allow-list as a single
// value, because Vite before 8.3 doesn't split the variable on commas. A leading dot
// makes it match every name under that domain, which covers <slug>.<base>,
// <slug>-dev.<base> and extra domains under the base.
func (c NodeConfig) Env(allowedHost string) []string {
	port := strconv.Itoa(c.Port)
	env := []string{"HOST=0.0.0.0", "PORT=" + port, "NITRO_HOST=0.0.0.0", "NITRO_PORT=" + port}
	if allowedHost != "" {
		// Vite ≥ 6 rejects unknown Host headers unless allowed.
		env = append(env, "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS="+allowedHost)
	}
	return env
}

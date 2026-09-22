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
	// PackageManager is npm, pnpm or yarn.
	PackageManager string `json:"packageManager,omitempty"`
	// Script is the package.json script to run (default "dev").
	Script string `json:"script,omitempty"`
	// Port the dev server listens on inside the container (default: the preset's port).
	Port int `json:"port,omitempty"`
	// Preset selects how host/port are passed: "vite", "next", "nuxt" or "generic" (env only).
	Preset string `json:"preset,omitempty"`
	// HostPort publishes the dev server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
}

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

// waitForPackageJSON is the sh script wrapped around the dev-server command when the Node
// container is the project's application: a blank project has no package.json yet and
// "npm run dev" would crash-loop. The validated argv follows as "$@" (after $0), so nothing
// is interpolated into the script.
const waitForPackageJSON = `until [ -f package.json ]; do echo 'envoryx: waiting for package.json in /var/www/html - scaffold with a Node template, clone a repository or use the Node terminal'; sleep 5; done; exec "$@"`

// Normalize validates the configuration and fills defaults.
func (c *NodeConfig) Normalize() error {
	if !c.DevServer {
		c.PackageManager, c.Script, c.Port, c.Preset = "", "", 0, ""
		return nil
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
	c.Script = strings.TrimSpace(c.Script)
	if c.Script == "" {
		c.Script = "dev"
	}
	if !scriptRe.MatchString(c.Script) {
		return fmt.Errorf("%w: invalid script name %q", validate.ErrInvalid, c.Script)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "vite"
	}
	preset, ok := NodePresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown dev server preset %q", validate.ErrInvalid, c.Preset)
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: dev server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return nil
}

// Command returns the argv of the dev server process.
func (c NodeConfig) Command() []string {
	cmd := []string{c.PackageManager, "run", c.Script}
	if c.PackageManager == "yarn" {
		cmd = []string{"yarn", c.Script}
	}
	port := strconv.Itoa(c.Port)
	switch c.Preset {
	case "vite":
		cmd = append(cmd, "--", "--host", "0.0.0.0", "--port", port, "--strictPort")
	case "next":
		cmd = append(cmd, "--", "-H", "0.0.0.0", "-p", port)
	case "nuxt":
		cmd = append(cmd, "--", "--host", "0.0.0.0", "--port", port)
	}
	return cmd
}

// WrappedCommand returns Command() behind the package.json wait guard, for containers
// whose dev server is the project's application.
func (c NodeConfig) WrappedCommand() []string {
	return append([]string{"sh", "-c", waitForPackageJSON, "envoryx-dev"}, c.Command()...)
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

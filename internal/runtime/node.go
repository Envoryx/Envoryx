package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/seramos/staqio/internal/validate"
)

// NodeConfig configures the Node.js service. Without DevServer the container idles as a
// tooling container; with it the container runs the package.json script as its main
// process and the embedded proxy routes <slug>-dev.<base> to it.
type NodeConfig struct {
	DevServer bool `json:"devServer"`
	// PackageManager is npm, pnpm or yarn.
	PackageManager string `json:"packageManager,omitempty"`
	// Script is the package.json script to run (default "dev").
	Script string `json:"script,omitempty"`
	// Port the dev server listens on inside the container (default 5173).
	Port int `json:"port,omitempty"`
	// Preset selects how host/port are passed: "vite", "next" or "generic" (env only).
	Preset string `json:"preset,omitempty"`
	// HostPort publishes the dev server on the Docker host (assigned by Staqio).
	HostPort int `json:"hostPort,omitempty"`
}

var scriptRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)

// NodePresets lists the supported dev-server presets (key → label).
var NodePresets = map[string]string{"vite": "Vite (Laravel, Vue, React, Svelte…)", "next": "Next.js", "generic": "Other (HOST/PORT env only)"}

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
	if c.Port == 0 {
		c.Port = 5173
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: dev server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "vite"
	}
	if _, ok := NodePresets[c.Preset]; !ok {
		return fmt.Errorf("%w: unknown dev server preset %q", validate.ErrInvalid, c.Preset)
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
	}
	return cmd
}

// Env returns the variables that make common dev servers listen on all interfaces and
// accept the proxied host name.
func (c NodeConfig) Env(devHost string) []string {
	port := strconv.Itoa(c.Port)
	env := []string{"HOST=0.0.0.0", "PORT=" + port, "NITRO_HOST=0.0.0.0", "NITRO_PORT=" + port}
	if devHost != "" {
		// Vite ≥ 6 rejects unknown Host headers unless allowed.
		env = append(env, "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS="+devHost)
	}
	return env
}

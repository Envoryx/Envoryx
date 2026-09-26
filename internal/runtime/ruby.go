package runtime

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// RubyConfig configures the Ruby service. Without Server the container idles as a
// tooling container (bundle, rake, rails, irb); with it the container runs the preset's
// application server as its main process. A project without PHP is then served by it:
// the embedded proxy routes the project's primary hostname to the Ruby container and the
// web container's port stays unpublished.
type RubyConfig struct {
	Server bool `json:"server"`
	// Mode is "dev" (RAILS_ENV/RACK_ENV development, Rails reloads code itself, default)
	// or "production": the environments switched to production, Rails served by Puma.
	Mode string `json:"mode,omitempty"`
	// Preset selects the server command: "rails" (bin/rails server in dev, Puma in
	// production) or "rack" (Puma on config.ru: Sinatra, Roda, Hanami …).
	Preset string `json:"preset,omitempty"`
	// Port the server listens on inside the container (default: the preset's port).
	Port int `json:"port,omitempty"`
	// HostPort publishes the server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
	// Debug runs the server under rdbg (the debug gem) listening for an IDE; without
	// Server only the port is published, for an rdbg started from the terminal.
	Debug bool `json:"debug,omitempty"`
	// DebugPort is rdbg's port inside the container (default 12345).
	DebugPort int `json:"debugPort,omitempty"`
	// DebugHostPort publishes rdbg on the Docker host (assigned by Envoryx).
	DebugHostPort int `json:"debugHostPort,omitempty"`
}

// Ruby run modes and defaults.
const (
	RubyModeDev        = "dev"
	RubyModeProduction = "production"
	// DefaultRdbgPort is the port the debug gem's documentation uses for rdbg --open.
	DefaultRdbgPort = 12345
	// railsPidFile is where bin/rails server writes its pid – inside the container, not in
	// the project's tmp/pids, and removed before every start: a pid file left behind by a
	// killed container makes Rails refuse to start ("A server is already running").
	railsPidFile = "/tmp/envoryx-rails.pid"
)

// RubyPreset describes a supported application server and its defaults.
type RubyPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// RubyPresets lists the supported presets in UI order.
var RubyPresets = []RubyPreset{
	{Key: "rails", Label: "Rails", Port: 3000},
	{Key: "rack", Label: "Rack (Puma on config.ru: Sinatra, Roda, Hanami …)", Port: 9292},
}

// RubyPresetByKey looks up a preset.
func RubyPresetByKey(key string) (RubyPreset, bool) {
	for _, p := range RubyPresets {
		if p.Key == key {
			return p, true
		}
	}
	return RubyPreset{}, false
}

// Normalize validates the configuration and fills defaults.
func (c *RubyConfig) Normalize() error {
	if !c.Server {
		// A tooling container: the rdbg port stays on its own for `rdbg --open` started
		// from the terminal (a rake task, a test run). Everything else is server
		// configuration.
		*c = RubyConfig{Debug: c.Debug, DebugPort: c.DebugPort, DebugHostPort: c.DebugHostPort}
		return c.normalizeDebug()
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", RubyModeDev:
		c.Mode = RubyModeDev
	case RubyModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "rails"
	}
	preset, ok := RubyPresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown Ruby server preset %q", validate.ErrInvalid, c.Preset)
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return c.normalizeDebug()
}

// normalizeDebug validates the rdbg port, which is published with or without the
// application server.
func (c *RubyConfig) normalizeDebug() error {
	if !c.Debug {
		c.DebugPort, c.DebugHostPort = 0, 0
		return nil
	}
	if c.DebugPort == 0 {
		c.DebugPort = DefaultRdbgPort
	}
	if c.DebugPort < 1024 || c.DebugPort > 65535 {
		return fmt.Errorf("%w: rdbg port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if c.Server && c.DebugPort == c.Port {
		return fmt.Errorf("%w: rdbg port must differ from the server port", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the server runs in the production environment.
func (c RubyConfig) Production() bool { return c.Mode == RubyModeProduction }

// server is the preset's server command. Puma binds what -b says: a config/puma.rb's own
// port setting does not add a second listener, command-line binds replace it.
func (c RubyConfig) server() []string {
	port := strconv.Itoa(c.Port)
	if c.Preset == "rails" && !c.Production() {
		return []string{"bin/rails", "server", "-b", "0.0.0.0", "-p", port, "-P", railsPidFile}
	}
	return []string{"bundle", "exec", "puma", "-b", "tcp://0.0.0.0:" + port}
}

// rdbgScript starts the command after it under rdbg listening on port $1. A bundle that
// locks the debug gem (Rails ships it) gets its own rdbg through bundle exec – two copies
// of the gem in one process would clash; otherwise the image's rdbg runs, which loads
// itself through RUBYOPT and so works under bundle exec as well.
const rdbgScript = `port=$1; shift
set -- --open --host=0.0.0.0 --port="$port" --nonstop -c -- "$@"
if [ -f Gemfile.lock ] && grep -qE '^    debug \(' Gemfile.lock; then exec bundle exec rdbg "$@"; fi
exec rdbg "$@"`

// Command returns the argv of the container's main process: the preset's server bound to
// all interfaces on Port, under rdbg when Debug is on. Every value comes from Normalize,
// nothing is interpolated into a script.
func (c RubyConfig) Command() []string {
	if c.Debug {
		return append([]string{"sh", "-c", rdbgScript, "envoryx-rdbg", strconv.Itoa(c.DebugPort)}, c.server()...)
	}
	return c.server()
}

// entryGuard blocks until the project has something to run: a blank project would send
// the server into a crash-loop.
func (c RubyConfig) entryGuard() string {
	test, what := "[ -e Gemfile ] && [ -e config.ru ]", "Gemfile and config.ru"
	if c.Preset == "rails" {
		test, what = "[ -e Gemfile ] && [ -e bin/rails ]", "Gemfile and bin/rails"
	}
	return fmt.Sprintf(`until %s; do echo 'envoryx: waiting for %s in /var/www/html - scaffold with a Ruby template, clone a repository or use the Ruby terminal'; sleep 5; done`, test, what)
}

// BundleGuard installs the bundle when it is not complete – after a clone, a changed
// Gemfile or a Ruby upgrade – and waits instead of crash-looping while that fails. Shared
// with the worker containers; a script without a Gemfile starts right away.
const BundleGuard = `if [ -e Gemfile ]; then until bundle check >/dev/null 2>&1 || bundle install; do echo 'envoryx: bundle install failed - fix the Gemfile (see above), retrying in 30 s'; sleep 30; done; fi`

// WrappedCommand returns Command() behind the entry-file guard and the bundle install,
// for containers whose server is the project's application. Further guards (the database
// wait the planner builds) run after them.
func (c RubyConfig) WrappedCommand(guards ...string) []string {
	own := []string{c.entryGuard(), BundleGuard}
	if c.Preset == "rails" && !c.Production() {
		own = append(own, "rm -f "+railsPidFile)
	}
	return Guarded(c.Command(), "envoryx-serve", append(own, guards...)...)
}

// Env returns the variables that tell the application where to listen and in which
// environment it runs: RAILS_ENV, RACK_ENV, APP_ENV (Sinatra) and HANAMI_ENV follow the
// mode. In development Rails only answers host names it knows – allowedHost goes into
// RAILS_DEVELOPMENT_HOSTS, where a leading dot allows every name under the domain. In
// production Rails serves public/ itself (there is no nginx in front of Puma).
func (c RubyConfig) Env(allowedHost string) []string {
	env := []string{"HOST=0.0.0.0", "PORT=" + strconv.Itoa(c.Port), "BINDING=0.0.0.0", "RAILS_LOG_TO_STDOUT=1"}
	if c.Production() {
		env = append(env, "RAILS_SERVE_STATIC_FILES=1")
	} else if allowedHost != "" {
		env = append(env, "RAILS_DEVELOPMENT_HOSTS="+allowedHost)
	}
	return append(env, c.AppEnv()...)
}

// AppEnv names the application environment for Rails, Rack, Sinatra and Hanami:
// development, or production in production mode. The workers get it, too.
func (c RubyConfig) AppEnv() []string {
	name := "development"
	if c.Production() {
		name = "production"
	}
	return []string{"RAILS_ENV=" + name, "RACK_ENV=" + name, "APP_ENV=" + name, "HANAMI_ENV=" + name}
}

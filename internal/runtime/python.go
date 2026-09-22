package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// PythonConfig configures the Python service. Without Server the container idles as a
// tooling container (pip, uv, the interpreter); with it the container runs the
// application server of the preset as its main process. A project without PHP is then
// served by it: the embedded proxy routes the project's primary hostname to the Python
// container and the web container's port stays unpublished.
type PythonConfig struct {
	Server bool `json:"server"`
	// Mode is "dev" (auto-reload, debug pages, default) or "production": the same preset
	// without reload – gunicorn for Django and Flask, uvicorn for ASGI apps.
	Mode string `json:"mode,omitempty"`
	// Preset selects the server command: "django" (manage.py runserver / gunicorn), "flask"
	// (flask run / gunicorn), "asgi" (uvicorn: FastAPI, Starlette, Litestar…), "wsgi"
	// (gunicorn for any WSGI app) or "module" (python -m <App>, HOST/PORT env only).
	Preset string `json:"preset,omitempty"`
	// App is the import path of the application: "module:attribute" for Flask, ASGI and
	// WSGI (app:app, main:app), the WSGI module for Django in production mode
	// (config.wsgi:application) and the module for the "module" preset.
	App string `json:"app,omitempty"`
	// Port the server listens on inside the container (default: the preset's port).
	Port int `json:"port,omitempty"`
	// HostPort publishes the server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
	// Debug publishes the debugpy port so an IDE can attach a debugger. Only the port is
	// published; the application has to start debugpy (python -m debugpy --listen
	// 0.0.0.0:<port> …) – see the IDE tab.
	Debug bool `json:"debug,omitempty"`
	// DebugPort is the debugpy port inside the container (default 5678).
	DebugPort int `json:"debugPort,omitempty"`
	// DebugHostPort publishes debugpy on the Docker host (assigned by Envoryx).
	DebugHostPort int `json:"debugHostPort,omitempty"`
}

// Python run modes and defaults.
const (
	PythonModeDev        = "dev"
	PythonModeProduction = "production"
	// DefaultDebugpyPort is debugpy's default listen port.
	DefaultDebugpyPort = 5678
	// PythonVenv is the virtual environment inside the project directory: the container's
	// PATH starts with its bin/ so python, pip, gunicorn … resolve to it once it exists.
	PythonVenv = ".venv"
)

// appRe accepts a dotted module path with an optional ":attribute" (gunicorn/uvicorn
// notation). Nothing else, so the value can be interpolated into the wait guard.
var appRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*(:[A-Za-z_][A-Za-z0-9_]*)?$`)

// ValidAppPath reports whether s is an import path in appRe's form (module or
// module:attribute) of sensible length – shared with the worker presets.
func ValidAppPath(s string) bool { return len(s) <= 128 && appRe.MatchString(s) }

// PythonPreset describes a supported application server and its defaults.
type PythonPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
	// App is the default import path (empty = the preset needs none in dev mode).
	App string `json:"app"`
	// AppLabel/AppHint describe the App field in the UI.
	AppLabel string `json:"appLabel"`
	AppHint  string `json:"appHint"`
}

// PythonPresets lists the supported presets in UI order.
var PythonPresets = []PythonPreset{
	{Key: "django", Label: "Django", Port: 8000, App: "config.wsgi:application", AppLabel: "WSGI application (production mode)", AppHint: "e.g. config.wsgi:application – dev mode runs manage.py runserver"},
	{Key: "flask", Label: "Flask", Port: 5000, App: "app:app", AppLabel: "Application", AppHint: "module:attribute, e.g. app:app"},
	{Key: "asgi", Label: "FastAPI / ASGI (uvicorn)", Port: 8000, App: "main:app", AppLabel: "ASGI application", AppHint: "module:attribute, e.g. main:app"},
	{Key: "wsgi", Label: "WSGI (gunicorn)", Port: 8000, App: "app:app", AppLabel: "WSGI application", AppHint: "module:attribute, e.g. app:app"},
	{Key: "module", Label: "Other (python -m, HOST/PORT env only)", Port: 8000, App: "app", AppLabel: "Module", AppHint: "run as python -m <module>; listen on $HOST:$PORT"},
}

// PythonPresetByKey looks up a preset.
func PythonPresetByKey(key string) (PythonPreset, bool) {
	for _, p := range PythonPresets {
		if p.Key == key {
			return p, true
		}
	}
	return PythonPreset{}, false
}

// Normalize validates the configuration and fills defaults.
func (c *PythonConfig) Normalize() error {
	if !c.Server {
		// The container idles as a tooling container – but the debugger port stands on its
		// own. What a developer steps through most often is a management command, a test
		// run or a script started from the terminal, and debugpy is attached to whatever
		// process you launch, not to the container's main command. Everything else is
		// server configuration and goes.
		*c = PythonConfig{Debug: c.Debug, DebugPort: c.DebugPort, DebugHostPort: c.DebugHostPort}
		return c.normalizeDebug()
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", PythonModeDev:
		c.Mode = PythonModeDev
	case PythonModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "django"
	}
	preset, ok := PythonPresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown Python server preset %q", validate.ErrInvalid, c.Preset)
	}
	c.App = strings.TrimSpace(c.App)
	if c.App == "" {
		c.App = preset.App
	}
	if !ValidAppPath(c.App) {
		return fmt.Errorf("%w: invalid application path %q (use module:attribute, e.g. app:app)", validate.ErrInvalid, c.App)
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return c.normalizeDebug()
}

// normalizeDebug validates the debugpy port, which is published with or without the
// application server.
func (c *PythonConfig) normalizeDebug() error {
	if !c.Debug {
		c.DebugPort, c.DebugHostPort = 0, 0
		return nil
	}
	if c.DebugPort == 0 {
		c.DebugPort = DefaultDebugpyPort
	}
	if c.DebugPort < 1024 || c.DebugPort > 65535 {
		return fmt.Errorf("%w: debugpy port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if c.Server && c.DebugPort == c.Port {
		return fmt.Errorf("%w: debugpy port must differ from the server port", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the server runs without reload.
func (c PythonConfig) Production() bool { return c.Mode == PythonModeProduction }

// Command returns the argv of the container's main process: the preset's server bound to
// all interfaces on Port. Every value comes from Normalize (validated against appRe or a
// port range), nothing is shell-interpolated.
func (c PythonConfig) Command() []string {
	port := strconv.Itoa(c.Port)
	bind := "0.0.0.0:" + port
	switch c.Preset {
	case "django":
		if c.Production() {
			return []string{"gunicorn", c.App, "--bind", bind}
		}
		return []string{"python", "manage.py", "runserver", bind}
	case "flask":
		if c.Production() {
			return []string{"gunicorn", c.App, "--bind", bind}
		}
		return []string{"flask", "--app", c.App, "run", "--host", "0.0.0.0", "--port", port, "--debug"}
	case "asgi":
		cmd := []string{"uvicorn", c.App, "--host", "0.0.0.0", "--port", port}
		if !c.Production() {
			cmd = append(cmd, "--reload")
		}
		return cmd
	case "wsgi":
		cmd := []string{"gunicorn", c.App, "--bind", bind}
		if !c.Production() {
			cmd = append(cmd, "--reload")
		}
		return cmd
	default: // module
		return []string{"python", "-m", c.App}
	}
}

// entry names what shows there is an application to run: manage.py for Django, else the
// first component of the import path as a module (<name>.py) or a package (<name>/). The
// name matched appRe, so it is safe inside the shell test of WrappedCommand.
func (c PythonConfig) entry() (module string, django bool) {
	if c.Preset == "django" {
		return "", true
	}
	mod := c.App
	if i := strings.IndexAny(mod, ".:"); i >= 0 {
		mod = mod[:i]
	}
	return mod, false
}

// WrappedCommand returns Command() behind a wait guard, for containers whose server is the
// project's application: a blank project has nothing to run yet and the server would
// crash-loop. The validated argv follows as "$@" (after $0), nothing is interpolated but
// the entry file name, which matched appRe.
func (c PythonConfig) WrappedCommand() []string {
	mod, django := c.entry()
	test, what := fmt.Sprintf("[ -e %s.py ] || [ -d %s ]", mod, mod), mod+".py or "+mod+"/"
	if django {
		test, what = "[ -e manage.py ]", "manage.py"
	}
	script := fmt.Sprintf(`until %s; do echo 'envoryx: waiting for %s in /var/www/html - scaffold with a Python template, clone a repository or use the Python terminal'; sleep 5; done; exec "$@"`, test, what)
	return append([]string{"sh", "-c", script, "envoryx-serve"}, c.Command()...)
}

// Env returns the variables that tell the application where to listen: HOST and PORT are
// read by the "module" preset and by common settings modules. FLASK_DEBUG and
// DJANGO_DEBUG follow the mode, so "production" really does run without the debugger and
// its error pages – the Django template wires DEBUG to DJANGO_DEBUG, and a project that
// does not read it simply ignores the variable.
func (c PythonConfig) Env() []string {
	env := []string{"HOST=0.0.0.0", "PORT=" + strconv.Itoa(c.Port)}
	if c.Production() {
		return append(env, "FLASK_DEBUG=0", "DJANGO_DEBUG=0")
	}
	return append(env, "FLASK_DEBUG=1", "DJANGO_DEBUG=1")
}

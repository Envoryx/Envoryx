package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// GoConfig configures the Go service. Without Server the container idles as a tooling
// container (go build, go test, go mod …); with it the container builds and runs the
// project's main package as its main process. A project without PHP is then served by
// it: the embedded proxy routes the project's primary hostname to the Go container and
// the web container's port stays unpublished.
type GoConfig struct {
	Server bool `json:"server"`
	// Mode is "dev" (air rebuilds and restarts on every change, default) or "production":
	// one build, then the binary.
	Mode string `json:"mode,omitempty"`
	// Package is the main package to build, relative to the project: "." (default) or a
	// path such as "./cmd/server".
	Package string `json:"package,omitempty"`
	// Port the server listens on inside the container (default 8080); the application
	// reads it from $PORT.
	Port int `json:"port,omitempty"`
	// HostPort publishes the server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
	// Debug runs the server under Delve (headless) so an IDE attaches to it; without
	// Server only the port is published, for a dlv started from the terminal.
	Debug bool `json:"debug,omitempty"`
	// DebugPort is Delve's port inside the container (default 2345).
	DebugPort int `json:"debugPort,omitempty"`
	// DebugHostPort publishes Delve on the Docker host (assigned by Envoryx).
	DebugHostPort int `json:"debugHostPort,omitempty"`
}

// Go run modes and defaults.
const (
	GoModeDev        = "dev"
	GoModeProduction = "production"
	DefaultGoPort    = 8080
	// DefaultDelvePort is Delve's customary headless port.
	DefaultDelvePort = 2345
	// goBuildDir is where the server binary is built – inside the container, never in the
	// project directory.
	goBuildDir = "/tmp/envoryx-go"
)

// goPackageRe accepts "." or a relative path below it; nothing else, so the value can go
// into the build command.
var goPackageRe = regexp.MustCompile(`^\.(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// ValidGoPackage reports whether s is a main package path in goPackageRe's form.
func ValidGoPackage(s string) bool {
	return len(s) <= 128 && goPackageRe.MatchString(s) && !strings.Contains(s, "/..") && !strings.Contains(s, "/.")
}

// Normalize validates the configuration and fills defaults.
func (c *GoConfig) Normalize() error {
	if !c.Server {
		// A tooling container: the Delve port stays on its own for `dlv debug` or `dlv
		// test` started from the terminal. Everything else is server configuration.
		*c = GoConfig{Debug: c.Debug, DebugPort: c.DebugPort, DebugHostPort: c.DebugHostPort}
		return c.normalizeDebug()
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", GoModeDev:
		c.Mode = GoModeDev
	case GoModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.Package = strings.TrimSuffix(strings.TrimSpace(c.Package), "/")
	if c.Package == "" {
		c.Package = "."
	}
	if !strings.HasPrefix(c.Package, ".") {
		c.Package = "./" + c.Package
	}
	if !ValidGoPackage(c.Package) {
		return fmt.Errorf("%w: invalid main package %q (use . or a path like ./cmd/server)", validate.ErrInvalid, c.Package)
	}
	if c.Port == 0 {
		c.Port = DefaultGoPort
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return c.normalizeDebug()
}

// normalizeDebug validates the Delve port, which is published with or without the
// application server.
func (c *GoConfig) normalizeDebug() error {
	if !c.Debug {
		c.DebugPort, c.DebugHostPort = 0, 0
		return nil
	}
	if c.DebugPort == 0 {
		c.DebugPort = DefaultDelvePort
	}
	if c.DebugPort < 1024 || c.DebugPort > 65535 {
		return fmt.Errorf("%w: Delve port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if c.Server && c.DebugPort == c.Port {
		return fmt.Errorf("%w: Delve port must differ from the server port", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the server is built once instead of rebuilt on changes.
func (c GoConfig) Production() bool { return c.Mode == GoModeProduction }

// buildArgs is the go build line of the server binary; a debug build keeps what Delve
// needs (no optimisation, no inlining).
func (c GoConfig) buildArgs() []string {
	args := []string{"go", "build"}
	if c.Debug {
		args = append(args, "-gcflags=all=-N -l")
	}
	return append(args, "-o", goBuildDir+"/app", c.Package)
}

// delve runs the built binary under a headless Delve that keeps the program running and
// accepts the IDE's reconnects.
func (c GoConfig) delve() []string {
	return []string{"dlv", "exec", "--headless", "--listen=:" + strconv.Itoa(c.DebugPort), "--api-version=2", "--accept-multiclient", "--continue", goBuildDir + "/app"}
}

// goPortsFreeScript waits (up to ten seconds) until nothing listens on the ports given
// as arguments, then execs the command after "--". air starts the new build right after
// killing the old one, and a Delve that is still tearing down its debuggee holds both
// ports a moment longer – the new dlv would fail with "address already in use" and the
// server stay down until the next change. The image has no ss or nc, so it reads
// /proc/net/tcp{,6} (state 0A is LISTEN).
const goPortsFreeScript = `i=0
while [ "$1" != -- ]; do
  p=$(printf '%04X' "$1")
  while [ $i -lt 50 ] && grep -qs ":$p [0-9A-F]*:[0-9A-F]* 0A" /proc/net/tcp /proc/net/tcp6; do sleep 0.2; i=$((i+1)); done
  shift
done
shift
exec "$@"`

// delveAfterRestart is delve() behind goPortsFreeScript, for air's build.full_bin.
func (c GoConfig) delveAfterRestart() []string {
	return append([]string{"sh", "-c", goPortsFreeScript, "envoryx-dlv", strconv.Itoa(c.DebugPort), strconv.Itoa(c.Port), "--"}, c.delve()...)
}

// Command returns the argv of the container's main process. In dev mode it is air –
// with the project's own .air.toml when there is one, else with flags that build Package
// into goBuildDir (nothing lands in the project directory); in production mode one build
// and the binary (goProductionScript). Every value comes from Normalize and arrives as an
// argument, nothing is interpolated into a script.
func (c GoConfig) Command() []string {
	if c.Production() {
		mode := "run"
		if c.Debug {
			mode = "debug"
		}
		return []string{"sh", "-c", goProductionScript, "envoryx-go-build", mode, c.Package, strconv.Itoa(c.DebugPort)}
	}
	air := []string{"air", "--tmp_dir", goBuildDir, "--build.cmd", shellJoin(c.buildArgs()), "--build.bin", goBuildDir + "/app",
		"--build.exclude_dir", "vendor,node_modules,.git,tmp,testdata"}
	if c.Debug {
		air = append(air, "--build.full_bin", shellJoin(c.delveAfterRestart()))
	}
	// A project that configures air itself keeps its configuration.
	return append([]string{"sh", "-c", `if [ -f .air.toml ]; then exec air; fi; exec "$@"`, "envoryx-air"}, air...)
}

// goProductionScript builds the main package ($2) once and runs it – under a headless
// Delve on port $3 when $1 is "debug". The values arrive as arguments, nothing is
// interpolated into the script.
const goProductionScript = `set -e
if [ "$1" = debug ]; then
  go build -gcflags='all=-N -l' -o ` + goBuildDir + `/app "$2"
  exec dlv exec --headless --listen=":$3" --api-version=2 --accept-multiclient --continue ` + goBuildDir + `/app
fi
go build -o ` + goBuildDir + `/app "$2"
exec ` + goBuildDir + `/app`

// shellJoin quotes argv for air's build.cmd / build.full_bin, which air runs through a
// shell. The values are validated (package path, port) or fixed, but quoting keeps a
// "-gcflags=all=-N -l" one argument.
func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,", r))
		}) < 0 {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// entryGuard blocks until the project has a go.mod: a blank project would send the build
// into a crash-loop.
func (c GoConfig) entryGuard() string {
	return `until [ -e go.mod ]; do echo 'envoryx: waiting for go.mod in /var/www/html - scaffold with a Go template, clone a repository or run go mod init in the Go terminal'; sleep 5; done`
}

// WrappedCommand returns Command() behind the go.mod guard, for containers whose server
// is the project's application. Further guards (the database wait the planner builds)
// run after it.
func (c GoConfig) WrappedCommand(guards ...string) []string {
	return Guarded(c.Command(), "envoryx-serve", append([]string{c.entryGuard()}, guards...)...)
}

// Env returns the variables that tell the application where to listen – HOST and PORT –
// and GIN_MODE, which Gin reads to switch its debug output off in production.
func (c GoConfig) Env() []string {
	env := []string{"HOST=0.0.0.0", "PORT=" + strconv.Itoa(c.Port)}
	if c.Production() {
		return append(env, "GIN_MODE=release")
	}
	return append(env, "GIN_MODE=debug")
}

package runtime

import (
	"fmt"
	"strings"
)

// Guards are the shell commands an application container runs before its server starts:
// they block until the project is actually ready for it - the entry file exists, the
// database accepts connections. Waiting beats crash-looping. A container that restarts
// every few seconds buries its real reason in a wall of logs, and Django's runserver does
// not even exit: its autoreload parent survives the failed child, so the container stays
// "running" and serves nothing until someone restarts it by hand.

// Guarded returns argv behind the given guards: a shell that runs them in order and then
// replaces itself with argv. The validated argv arrives as "$@" (after $0), so nothing in
// it is interpolated into the script. Empty guards are dropped, and argv comes back
// unchanged when none is left - a project without a database keeps the exact command it
// had, so its container is not recreated for nothing.
func Guarded(argv []string, name string, guards ...string) []string {
	kept := make([]string, 0, len(guards))
	for _, g := range guards {
		if strings.TrimSpace(g) != "" {
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		return argv
	}
	script := strings.Join(kept, "; ") + `; exec "$@"`
	return append([]string{"sh", "-c", script, name}, argv...)
}

// WaitingFile is where a guard notes, while it waits, a problem only the user can solve:
// one of the Wait* keys. The server's health check fails while the file has content, so
// the status shows the service as waiting instead of green (see ServerHealthTest), and
// Supervised removes the file before the server starts. It lives in the container's own
// /tmp, which a recreated container starts without.
const WaitingFile = "/tmp/envoryx-waiting"

// The keys a guard writes to WaitingFile. The status turns them into a message; the
// Python one is followed by ":" and the module the server waits for.
const (
	WaitBundle       = "bundle-failed"
	WaitNodeDeps     = "node-deps"
	WaitVenv         = "python-venv"
	WaitPythonDeps   = "python-deps"
	WaitServerExited = "server-exited"
	WaitGoMod        = "go-mod"
)

// The shell commands that record a key in WaitingFile, constants for the constant guards.
const (
	noteBundle       = "echo " + WaitBundle + " >" + WaitingFile
	noteNodeDeps     = "echo " + WaitNodeDeps + " >" + WaitingFile
	noteVenv         = "echo " + WaitVenv + " >" + WaitingFile
	noteServerExited = "echo " + WaitServerExited + " >" + WaitingFile
	noteGoMod        = "echo " + WaitGoMod + " >" + WaitingFile
)

// ActionEnv marks the processes of an action: RunAction sets it, and Supervised looks for
// it in /proc to tell whether one is still running in the container.
const ActionEnv = "ENVORYX_ACTION"

// actionRunning succeeds while a process of an action runs in the container. Actions run
// as the same user as the server, so their environment is readable; the shell itself and
// grep don't have the variable.
const actionRunning = `grep -qsz '^` + ActionEnv + `=' /proc/[0-9]*/environ`

// supervise runs "$@" as a child of the shell instead of replacing it, and passes a stop
// on. When the server exits, the shell stays for serverRestartDelay seconds and as long
// as an action is running, then exits with the server's status, so Docker restarts the
// container as before. The container's main process ending would take a running action
// with it: pip install after an import the server could not load yet, typically - the
// server crashes on the import, and the install that fixes it is killed. Holding on also
// keeps a crash-looping container up: having run 10 seconds, Docker restarts it right
// away instead of with an ever longer delay, so the action that fixes the crash can be
// started in it at all. While an action holds it, the waiting note says the server is
// down.
const supervise = `rm -f ` + WaitingFile + `; "$@" & envoryx_pid=$!; ` +
	`trap 'envoryx_stop=1; kill -TERM $envoryx_pid 2>/dev/null' TERM INT; ` +
	`while :; do wait $envoryx_pid; envoryx_rc=$?; kill -0 $envoryx_pid 2>/dev/null || break; done; ` +
	`if [ -z "$envoryx_stop" ]; then echo "envoryx: the server exited (status $envoryx_rc) - it starts again in ` + serverRestartDelay + ` s, or once a running action has finished"; envoryx_i=0; ` +
	`while [ -z "$envoryx_stop" ]; do if ` + actionRunning + `; then ` + noteServerExited + `; elif [ $envoryx_i -ge ` + serverRestartDelay + ` ]; then break; fi; sleep 1; envoryx_i=$((envoryx_i+1)); done; fi; ` +
	`exit $envoryx_rc`

// serverRestartDelay is how many seconds a server's container stays up after the server
// exited: 10, the run time after which Docker resets its restart delay.
const serverRestartDelay = "10"

// Supervised is Guarded for the containers whose main process is the project's server:
// the guards run in order, then the shell starts argv and stays as its supervisor (see
// supervise). A note a guard left from before a restart is cleared first.
func Supervised(argv []string, name string, guards ...string) []string {
	kept := []string{"rm -f " + WaitingFile}
	for _, g := range guards {
		if strings.TrimSpace(g) != "" {
			kept = append(kept, g)
		}
	}
	script := strings.Join(kept, "; ") + "; " + supervise
	return append([]string{"sh", "-c", script, name}, argv...)
}

// ServerHealthTest is the health check of a server container: it fails, printing the
// key, while a guard notes a problem in WaitingFile.
var ServerHealthTest = []string{"sh", "-c", `[ ! -s ` + WaitingFile + ` ] || { cat ` + WaitingFile + `; exit 1; }`}

// WaitForTCP is a guard that blocks until host:port accepts a connection. socat ships in
// every Envoryx runtime image. Putting host into the shell line is safe: it's either one
// of our fixed network aliases or an external address that passed
// NormalizeExternalDatabase. The database images only open their port once their setup
// is done (until then they run on a socket or with networking off), so a connection
// really means the database is ready.
func WaitForTCP(host string, port int, label string) string {
	addr := host
	if strings.Contains(host, ":") {
		addr = "[" + host + "]" // IPv6, as socat wants it
	}
	return fmt.Sprintf(`until socat -u /dev/null TCP:%s:%d 2>/dev/null; do echo 'envoryx: waiting for %s at %s:%d'; sleep 2; done`, addr, port, label, host, port)
}

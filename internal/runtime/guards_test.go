package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// supervisedCmd runs Supervised's shell on the host, with the waiting note in a temporary
// file instead of /tmp/envoryx-waiting and the restart delay cut to 1 second.
func supervisedCmd(t *testing.T, argv ...string) (*exec.Cmd, string) {
	t.Helper()
	note := filepath.Join(t.TempDir(), "waiting")
	cmd := Supervised(argv, "envoryx-test", "true")
	cmd[2] = strings.ReplaceAll(cmd[2], WaitingFile, note)
	cmd[2] = strings.ReplaceAll(cmd[2], "-ge "+serverRestartDelay+" ]", "-ge 1 ]")
	return exec.Command(cmd[0], cmd[1:]...), note
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var ee *exec.ExitError
	if err == nil {
		return 0
	}
	if !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return ee.ExitCode()
}

func TestSupervisedPassesTheExitStatusOn(t *testing.T) {
	cmd, note := supervisedCmd(t, "sh", "-c", "exit 3")
	if err := os.WriteFile(note, []byte(WaitBundle+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if code := exitCode(t, cmd.Run()); code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if d := time.Since(begin); d < 900*time.Millisecond || d > 5*time.Second {
		t.Fatalf("the container must stay up for the restart delay after the server exited: %v", d)
	}
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Fatalf("the note of an earlier start must be cleared before the server starts: %v", err)
	}
}

// A server that dies while an action runs keeps the container's main process alive until
// the action is done, so Docker's restart doesn't kill the action.
func TestSupervisedWaitsForARunningAction(t *testing.T) {
	action := exec.Command("sleep", "3")
	action.Env = append(os.Environ(), ActionEnv+"=pip:install")
	if err := action.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = action.Process.Kill(); _ = action.Wait() })
	cmd, note := supervisedCmd(t, "sh", "-c", "exit 4")
	begin := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if b, err := os.ReadFile(note); err != nil || strings.TrimSpace(string(b)) != WaitServerExited {
		t.Errorf("while it waits, the note says the server exited: %q, %v", b, err)
	}
	if code := exitCode(t, cmd.Wait()); code != 4 {
		t.Fatalf("exit %d, want 4", code)
	}
	if d := time.Since(begin); d < 2500*time.Millisecond {
		t.Fatalf("exited after %v, before the action finished", d)
	}
}

func TestSupervisedForwardsAStop(t *testing.T) {
	cmd, _ := supervisedCmd(t, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	begin := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, cmd.Wait()); code == 0 {
		t.Fatal("a stopped server exits with its signal status")
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("the stop took %v: the server did not get the signal", d)
	}
}

func TestGuardsNoteWhatTheyWaitFor(t *testing.T) {
	for name, g := range map[string]string{"bundle": BundleGuard, "node": NodeConfig{}.DepsGuard(), "venv": VenvGuard, "deps": PythonConfig{Preset: "flask"}.DepsGuard()} {
		if !strings.Contains(g, " >"+WaitingFile) {
			t.Errorf("%s guard writes no note: %s", name, g)
		}
	}
	if g := (PythonConfig{Preset: "flask"}).DepsGuard(); !strings.Contains(g, "echo "+WaitPythonDeps+":flask >") {
		t.Errorf("the Python guard names the module it waits for: %s", g)
	}
}

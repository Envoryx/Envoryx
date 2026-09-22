package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
)

// execFrames runs a command over the API and returns the NDJSON frames it produced.
func (a *testApp) execFrames(path string, body any) (int, []map[string]any) {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, a.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "Envoryx")
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	var frames []map[string]any
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		var frame map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			a.t.Fatalf("frame %q: %v", scanner.Text(), err)
		}
		frames = append(frames, frame)
	}
	return res.StatusCode, frames
}

// A command's output arrives as it is produced and the exit code closes the stream, so a
// script can tell success from failure – which is what the terminal WebSocket cannot do.
func TestExecStreamsOutputAndExitCode(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{
		"name": "Shop", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()},
	}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	var gotCmd []string
	var gotStdin string
	a.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		gotCmd, gotStdin = cmd, string(stdin)
		return "ready\n", 7, nil
	}
	status, frames := a.execFrames("/api/v1/projects/"+id+"/services/php/exec",
		map[string]any{"cmd": []string{"php", "-r", "echo 1;"}, "stdin": "input\n"})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if len(frames) != 2 || frames[0]["type"] != "stdout" || frames[0]["text"] != "ready\n" {
		t.Fatalf("frames: %v", frames)
	}
	if frames[1]["type"] != "exit" || frames[1]["code"].(float64) != 7 {
		t.Fatalf("exit frame: %v", frames[1])
	}
	if strings.Join(gotCmd, " ") != "php -r echo 1;" || gotStdin != "input\n" {
		t.Fatalf("command %v, stdin %q", gotCmd, gotStdin)
	}

	// A refusal that happens before any output is an ordinary HTTP error.
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/services/php/exec", map[string]any{"cmd": []string{}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("empty command: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/services/database/exec", map[string]any{"cmd": []string{"psql"}}, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("absent service: %d %s", r.status, r.raw)
	}

	// Nothing has been written yet when the engine is gone, so the caller gets the
	// usual error envelope with a status it can branch on.
	a.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) {
		return "", 0, docker.ErrUnavailable
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/services/php/exec", map[string]any{"cmd": []string{"php"}}, true)
	if r.status != http.StatusServiceUnavailable || errCode(r) != "docker_unavailable" {
		t.Fatalf("engine failure: %d %s", r.status, r.raw)
	}
}

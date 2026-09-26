package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ollamaReply fakes Ollama's API behind the bash request script: argv ends in method,
// path and body; the handler returns the raw HTTP/1.0 answer.
func ollamaReply(t *testing.T, e *env, fn func(method, path, body string) string) {
	t.Helper()
	e.engine.StreamHandler = func(container string, cmd []string, _ []string, _ []byte) (string, int, error) {
		if !strings.HasSuffix(container, "-ollama") || len(cmd) != 7 || cmd[0] != "bash" {
			t.Errorf("unexpected exec in %s: %v", container, cmd)
			return "", 1, nil
		}
		return fn(cmd[4], cmd[5], cmd[6]), 0, nil
	}
}

func httpOK(body string) string {
	return "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\n\r\n" + body
}

func TestOllamaContainerSharesTheModelStore(t *testing.T) {
	e := newEnv(t)
	req := phpRequest("Chat", true)
	req.Ollama = &ExtraRequest{}
	view, err := e.m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ol, ok := e.engine.Container("envoryx-chat-ollama")
	if !ok {
		t.Fatal("no ollama container")
	}
	env := strings.Join(ol.Spec.Env, "\n")
	if ol.Spec.Image != "ollama/ollama:0.34.4" || ol.Spec.User != "1000:1000" || !strings.Contains(env, "OLLAMA_MODELS=/models") || !strings.Contains(env, "HOME=/tmp") {
		t.Fatalf("ollama container: %+v", ol.Spec)
	}
	if len(ol.Spec.Mounts) != 1 || ol.Spec.Mounts[0].Source != "/host/appdata/envoryx/ollama" || ol.Spec.Mounts[0].Target != "/models" {
		t.Fatalf("model store mount: %+v", ol.Spec.Mounts)
	}
	if ol.Spec.GPUs || len(ol.Spec.Ports) != 0 || !slices.Equal(ol.Spec.NetworkAlias, []string{"ollama"}) {
		t.Fatalf("defaults: gpu %v ports %v alias %v", ol.Spec.GPUs, ol.Spec.Ports, ol.Spec.NetworkAlias)
	}
	if fi, err := os.Stat(filepath.Join(e.cfgDir, "ollama")); err != nil || !fi.IsDir() {
		t.Fatalf("the shared store must be created: %v", err)
	}
	php, _ := e.engine.Container("envoryx-chat-php")
	phpEnv := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"OLLAMA_HOST=http://ollama:11434", "OLLAMA_BASE_URL=http://ollama:11434", "OLLAMA_URL=http://ollama:11434"} {
		if !strings.Contains(phpEnv, want) {
			t.Fatalf("php env lacks %s: %v", want, php.Spec.Env)
		}
	}
	extras, err := e.m.ExtraServices(context.Background(), view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(extras) != 1 || extras[0].Kind != store.ServiceOllama || extras[0].Host != "ollama" || extras[0].Port != 11434 || extras[0].VolumeName != "" || len(extras[0].InjectedEnv) != 3 {
		t.Fatalf("extras: %+v", extras)
	}
}

// The GPUs are handed over at creation: switching them recreates the container, and a
// project without them keeps its fingerprint.
func TestOllamaGPUSwitch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Chat GPU", true)
	req.Ollama = &ExtraRequest{GPU: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ol, _ := e.engine.Container("envoryx-chat-gpu-ollama")
	if !ol.Spec.GPUs {
		t.Fatal("GPU requested but not handed over")
	}
	withGPU := specFingerprint(ol.Spec)
	off := false
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Ollama: &ExtraUpdate{Enabled: true, GPU: &off}}); err != nil {
		t.Fatal(err)
	}
	ol, _ = e.engine.Container("envoryx-chat-gpu-ollama")
	if ol.Spec.GPUs || specFingerprint(ol.Spec) == withGPU {
		t.Fatalf("GPU switched off, container still has it: %+v", ol.Spec)
	}
	extras, _ := e.m.ExtraServices(ctx, view.Project.ID)
	if len(extras) != 1 || extras[0].GPU {
		t.Fatalf("extras after switching off: %+v", extras)
	}
	// Leaving GPU out of an update keeps it as it is.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Ollama: &ExtraUpdate{Enabled: true, ExposePort: true}}); err != nil {
		t.Fatal(err)
	}
	ol, _ = e.engine.Container("envoryx-chat-gpu-ollama")
	if ol.Spec.GPUs || len(ol.Spec.Ports) != 1 || ol.Spec.Ports[0].ContainerPort != 11434 {
		t.Fatalf("after publishing the port: %+v", ol.Spec)
	}
}

func TestOllamaModelsPullAndDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Chat Models", true)
	req.Ollama = &ExtraRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	var deleted []string
	ollamaReply(t, e, func(method, path, body string) string {
		switch method + " " + path {
		case "GET /api/tags":
			return httpOK(`{"models":[{"name":"qwen3:8b","size":5200000000,"modified_at":"2026-09-26T10:00:00Z","details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q4_K_M"}},{"name":"all-minilm:latest","size":45960996,"modified_at":"2026-09-26T09:00:00Z","details":{"family":"bert"}}]}`)
		case "POST /api/pull":
			if body != `{"model":"llama3.2","stream":true}` {
				t.Errorf("pull body %s", body)
			}
			return httpOK(`{"status":"pulling manifest"}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":400}
{"status":"pulling bbb","digest":"sha256:bbb","total":24,"completed":24}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":1000}
{"status":"verifying sha256 digest"}
{"status":"success"}
`)
		case "DELETE /api/delete":
			deleted = append(deleted, body)
			if strings.Contains(body, "missing") {
				return "HTTP/1.0 404 Not Found\r\nContent-Type: application/json\r\n\r\n{\"error\":\"model 'missing' not found\"}"
			}
			return httpOK("")
		}
		t.Errorf("unexpected request %s %s", method, path)
		return "HTTP/1.0 500 Internal Server Error\r\n\r\n"
	})

	list, err := e.m.OllamaModels(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 2 || list.Models[0].Name != "all-minilm:latest" || list.Models[1].ParameterSize != "8.2B" || list.Models[1].Quantization != "Q4_K_M" || list.Models[1].Size != 5200000000 {
		t.Fatalf("models: %+v", list.Models)
	}

	if _, err := e.m.PullOllamaModel(ctx, id, "llama3.2"); err != nil {
		t.Fatal(err)
	}
	var pull OllamaPull
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		list, err = e.m.OllamaModels(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Pulls) == 1 && list.Pulls[0].Done {
			pull = list.Pulls[0]
			break
		}
	}
	if pull.Status != "success" || pull.Total != 1024 || pull.Completed != 1024 || pull.Error != "" {
		t.Fatalf("pull: %+v", pull)
	}

	if err := e.m.DeleteOllamaModel(ctx, id, "qwen3:8b"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.DeleteOllamaModel(ctx, id, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting an unknown model: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != `{"model":"qwen3:8b"}` {
		t.Fatalf("delete bodies: %v", deleted)
	}
}

func TestOllamaPullFailureIsReported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Chat Fail", true)
	req.Ollama = &ExtraRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ollamaReply(t, e, func(method, path, _ string) string {
		if path == "/api/pull" {
			return httpOK(`{"status":"pulling manifest"}
{"error":"pull model manifest: file does not exist"}
`)
		}
		return httpOK(`{"models":[]}`)
	})
	if _, err := e.m.PullOllamaModel(ctx, view.Project.ID, "no-such-model"); err != nil {
		t.Fatal(err)
	}
	var pull OllamaPull
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		list, _ := e.m.OllamaModels(ctx, view.Project.ID)
		if len(list.Pulls) == 1 && list.Pulls[0].Done {
			pull = list.Pulls[0]
			break
		}
	}
	if pull.Status != "failed" || !strings.Contains(pull.Error, "file does not exist") {
		t.Fatalf("failed pull: %+v", pull)
	}
}

func TestOllamaNeedsTheServiceRunning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("No Ollama", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.OllamaModels(ctx, view.Project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project without Ollama: %v", err)
	}
	req := phpRequest("Stopped Ollama", false)
	req.Ollama = &ExtraRequest{}
	stopped, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.PullOllamaModel(ctx, stopped.Project.ID, "llama3.2"); err == nil {
		t.Fatal("a pull needs a running Ollama")
	}
}

func TestValidateOllamaModel(t *testing.T) {
	for _, ok := range []string{"llama3.2", "qwen3:8b", "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M", "library/mistral:7b-instruct-v0.3-q4_0"} {
		if err := ValidateOllamaModel(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-rf", "a b", "llama3.2;rm", "x:", "a//b", "ünicode", strings.Repeat("a", 201)} {
		if err := ValidateOllamaModel(bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestOllamaManifestRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Chat Manifest", false)
	req.Ollama = &ExtraRequest{GPU: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.m.loadProject(ctx, view.Project.ID)
	mf := exportState(p, nil, nil)
	if mf.Ollama == nil || !mf.Ollama.GPU || mf.Ollama.Version != "0.34" {
		t.Fatalf("exported ollama: %+v", mf.Ollama)
	}
	bad := manifest.Manifest{Version: 1, Redis: &manifest.Service{GPU: true}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "gpu belongs to ollama") {
		t.Fatalf("gpu on redis: %v", err)
	}
}

// Without a way to hand over GPUs the switch is refused before anything is stored, so
// Ollama keeps starting.
func TestOllamaGPURefusedWithoutToolkit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Chat No GPU", true)
	req.Ollama = &ExtraRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		if !spec.GPUs || spec.Entrypoint[0] != "true" {
			t.Errorf("gpu check spec: %+v", spec)
		}
		return docker.ExecResult{}, fmt.Errorf("%w (failed to discover GPU vendor from CDI)", docker.ErrNoGPU)
	}
	on := true
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Ollama: &ExtraUpdate{Enabled: true, GPU: &on}}); !errors.Is(err, docker.ErrNoGPU) {
		t.Fatalf("switching the GPU on without a toolkit: %v", err)
	}
	extras, _ := e.m.ExtraServices(ctx, view.Project.ID)
	if len(extras) != 1 || extras[0].GPU {
		t.Fatalf("the refused switch must not be stored: %+v", extras)
	}
	req = phpRequest("New With GPU", false)
	req.Ollama = &ExtraRequest{GPU: true}
	if _, err := e.m.Create(ctx, req); !errors.Is(err, docker.ErrNoGPU) {
		t.Fatalf("creating with a GPU without a toolkit: %v", err)
	}
}

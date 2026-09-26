// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// OllamaModel is one model in the shared store, as Ollama lists it.
type OllamaModel struct {
	Name          string    `json:"name"`
	Size          int64     `json:"size"`
	ModifiedAt    time.Time `json:"modifiedAt"`
	Family        string    `json:"family,omitempty"`
	ParameterSize string    `json:"parameterSize,omitempty"`
	Quantization  string    `json:"quantization,omitempty"`
}

// OllamaPull is a model download started from Envoryx. Completed and Total add up the
// model's layers; Total grows while Ollama learns about them.
type OllamaPull struct {
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	Completed int64     `json:"completed"`
	Total     int64     `json:"total"`
	Error     string    `json:"error,omitempty"`
	Done      bool      `json:"done"`
	StartedAt time.Time `json:"startedAt"`
}

// OllamaModels is what the UI shows: the store's models and this project's downloads.
type OllamaModels struct {
	Models []OllamaModel `json:"models"`
	Pulls  []OllamaPull  `json:"pulls"`
}

// ollamaModelRe accepts Ollama's model references: name[:tag], optionally below a
// namespace or registry host (hf.co/org/repo:Q4_K_M). ASCII only, so the request body
// has as many bytes as bash counts characters.
var ollamaModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*(:[A-Za-z0-9][A-Za-z0-9._-]*)?$`)

// ValidateOllamaModel checks a model reference.
func ValidateOllamaModel(name string) error {
	if len(name) > 200 || !ollamaModelRe.MatchString(name) {
		return fmt.Errorf("%w: %q is not a model name (e.g. llama3.2 or qwen3:8b)", validate.ErrInvalid, name)
	}
	return nil
}

// ollamaPullKeep is how long a finished download stays visible: a failure long enough to
// be read, a success until the next refresh has shown the model in the list.
const ollamaPullKeep = 10 * time.Minute

// ollamaPulls tracks the downloads in progress, per project and model.
type ollamaPulls struct {
	mu    sync.Mutex
	pulls map[string]*ollamaPullState
}

type ollamaPullState struct {
	OllamaPull
	projectID string
	cancel    context.CancelFunc
	// auditCtx carries the actor who started the download to its audit entry.
	auditCtx context.Context
	finished time.Time
	layers   map[string][2]int64 // digest -> completed, total
}

func pullKey(projectID, model string) string { return projectID + "\x00" + model }

func (m *Manager) pulls() *ollamaPulls {
	m.ollamaOnce.Do(func() { m.ollama = &ollamaPulls{pulls: map[string]*ollamaPullState{}} })
	return m.ollama
}

// ollamaContainer returns the running Ollama container of a project.
func (m *Manager) ollamaContainer(ctx context.Context, id string) (docker.Container, error) {
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return docker.Container{}, err
	}
	if svc := p.Service(store.ServiceOllama); svc == nil || !svc.Enabled {
		return docker.Container{}, fmt.Errorf("%w: the project has no Ollama", ErrNotFound)
	}
	c, err := m.ServiceContainer(ctx, id, store.ServiceOllama)
	if err != nil {
		return docker.Container{}, err
	}
	if c.State != "running" {
		return docker.Container{}, fmt.Errorf("%w: Ollama is not running; start the project first", ErrConflict)
	}
	return c, nil
}

// ollamaRequestScript sends one HTTP/1.0 request to Ollama's API from inside its
// container: the image has neither curl nor wget, but bash opens TCP connections itself.
// HTTP/1.0 keeps the answer unchunked, so a streamed reply arrives line by line and ends
// when Ollama closes the connection. Method, path and body arrive as arguments, never as
// part of the script.
//
// Docker leaves an exec running when its caller goes away, and Ollama keeps pulling as
// long as the connection is open. So the script reports its PID first, and cat replaces
// the shell: killing that one process closes the connection and ends the request.
var ollamaRequestScript = `echo "$$" >&2
exec 3<>/dev/tcp/127.0.0.1/` + strconv.Itoa(runtime.OllamaPort) + ` || exit 7
printf '%s %s HTTP/1.0\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s' "$1" "$2" "${#3}" "$3" >&3
exec cat <&3`

// ollamaAPI runs one request against a project's Ollama and hands the response to fn
// while it is still arriving. The response body must be read inside fn.
func (m *Manager) ollamaAPI(ctx context.Context, containerID, method, path string, body any, fn func(*http.Response) error) error {
	payload := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = string(raw)
	}
	pr, pw := io.Pipe()
	stderr := &execStderr{}
	execDone := make(chan error, 1)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			m.killOllamaRequest(containerID, stderr.pid())
		case <-finished:
		}
	}()
	go func() {
		code, err := m.engine.ExecStream(ctx, containerID, docker.ExecStreamOptions{
			Cmd:    []string{"bash", "-c", ollamaRequestScript, "ollama-api", method, path, payload},
			Env:    []string{"LC_ALL=C"},
			Stdout: pw,
			Stderr: stderr,
		})
		if err == nil && code != 0 {
			err = fmt.Errorf("the request to Ollama failed (exit %d): %s", code, stderr.message())
		}
		pw.CloseWithError(err)
		execDone <- err
	}()
	resp, err := http.ReadResponse(bufio.NewReader(pr), &http.Request{Method: method})
	if err != nil {
		pr.CloseWithError(err)
		if execErr := <-execDone; execErr != nil {
			return execErr
		}
		return fmt.Errorf("read Ollama's answer: %w", err)
	}
	fnErr := fn(resp)
	// Drain whatever fn left so the exec can finish, then report the first failure.
	_, _ = io.Copy(io.Discard, resp.Body)
	execErr := <-execDone
	if fnErr != nil {
		return fnErr
	}
	return execErr
}

// killOllamaRequest ends the request script with the given PID inside the container.
func (m *Manager) killOllamaRequest(containerID, pid string) {
	if _, err := strconv.Atoi(pid); err != nil {
		return // the script has not started yet; the exec fails on its own
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.engine.Exec(ctx, containerID, []string{"kill", pid}, nil); err != nil {
		m.log.Warn("could not end an Ollama request", "container", containerID, "pid", pid, "error", err)
	}
}

// execStderr collects the request script's stderr: its PID on the first line, then
// whatever went wrong.
type execStderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (e *execStderr) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buf.Write(p)
}

func (e *execStderr) pid() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	first, _, _ := bytes.Cut(e.buf.Bytes(), []byte("\n"))
	return string(bytes.TrimSpace(first))
}

func (e *execStderr) message() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, rest, _ := bytes.Cut(e.buf.Bytes(), []byte("\n"))
	return string(bytes.TrimSpace(rest))
}

// ollamaError turns an error reply ({"error":"…"}) into an error.
func ollamaError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrNotFound, e.Error)
		}
		return fmt.Errorf("ollama: %s", e.Error)
	}
	return fmt.Errorf("ollama answered %s", resp.Status)
}

// OllamaModels lists the models in the shared store and the project's downloads.
func (m *Manager) OllamaModels(ctx context.Context, id string) (OllamaModels, error) {
	out := OllamaModels{Models: []OllamaModel{}, Pulls: m.ollamaPullsOf(id)}
	c, err := m.ollamaContainer(ctx, id)
	if err != nil {
		return OllamaModels{}, err
	}
	err = m.ollamaAPI(ctx, c.ID, http.MethodGet, "/api/tags", nil, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return ollamaError(resp)
		}
		var tags struct {
			Models []struct {
				Name       string    `json:"name"`
				Size       int64     `json:"size"`
				ModifiedAt time.Time `json:"modified_at"`
				Details    struct {
					Family            string `json:"family"`
					ParameterSize     string `json:"parameter_size"`
					QuantizationLevel string `json:"quantization_level"`
				} `json:"details"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
			return fmt.Errorf("read Ollama's model list: %w", err)
		}
		for _, t := range tags.Models {
			out.Models = append(out.Models, OllamaModel{Name: t.Name, Size: t.Size, ModifiedAt: t.ModifiedAt, Family: t.Details.Family, ParameterSize: t.Details.ParameterSize, Quantization: t.Details.QuantizationLevel})
		}
		return nil
	})
	if err != nil {
		return OllamaModels{}, err
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Name < out.Models[j].Name })
	return out, nil
}

// ollamaPullsOf returns a project's downloads, newest first, and forgets finished ones
// that have been shown long enough.
func (m *Manager) ollamaPullsOf(id string) []OllamaPull {
	st := m.pulls()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := []OllamaPull{}
	for k, p := range st.pulls {
		if p.Done && time.Since(p.finished) > ollamaPullKeep {
			delete(st.pulls, k)
			continue
		}
		if p.projectID == id {
			out = append(out, p.OllamaPull)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// PullOllamaModel starts downloading a model into the shared store. It returns once the
// download runs; OllamaModels reports its progress. A model already being pulled for the
// project is not started twice.
func (m *Manager) PullOllamaModel(ctx context.Context, id, model string) (OllamaPull, error) {
	if err := ValidateOllamaModel(model); err != nil {
		return OllamaPull{}, err
	}
	c, err := m.ollamaContainer(ctx, id)
	if err != nil {
		return OllamaPull{}, err
	}
	st := m.pulls()
	st.mu.Lock()
	key := pullKey(id, model)
	if p := st.pulls[key]; p != nil && !p.Done {
		st.mu.Unlock()
		return OllamaPull{}, fmt.Errorf("%w: %s is already being downloaded", ErrConflict, model)
	}
	// The download outlives the request that started it; stopping the project or a
	// cancel ends it.
	pullCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &ollamaPullState{
		OllamaPull: OllamaPull{Model: model, Status: "starting", StartedAt: time.Now().UTC()},
		projectID:  id,
		cancel:     cancel,
		auditCtx:   context.WithoutCancel(ctx),
		layers:     map[string][2]int64{},
	}
	st.pulls[key] = p
	snapshot := p.OllamaPull
	st.mu.Unlock()

	go m.runOllamaPull(pullCtx, id, c.ID, p)
	return snapshot, nil
}

func (m *Manager) runOllamaPull(ctx context.Context, id, containerID string, p *ollamaPullState) {
	st := m.pulls()
	err := m.ollamaAPI(ctx, containerID, http.MethodPost, "/api/pull", map[string]any{"model": p.Model, "stream": true}, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return ollamaError(resp)
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var ev struct {
				Status    string `json:"status"`
				Digest    string `json:"digest"`
				Total     int64  `json:"total"`
				Completed int64  `json:"completed"`
				Error     string `json:"error"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			if ev.Error != "" {
				return errors.New(ev.Error)
			}
			st.mu.Lock()
			p.Status = ev.Status
			if ev.Digest != "" && ev.Total > 0 {
				p.layers[ev.Digest] = [2]int64{ev.Completed, ev.Total}
				p.Completed, p.Total = 0, 0
				for _, l := range p.layers {
					p.Completed += l[0]
					p.Total += l[1]
				}
			}
			st.mu.Unlock()
		}
		return sc.Err()
	})
	st.mu.Lock()
	p.Done, p.finished = true, time.Now()
	switch {
	case ctx.Err() != nil:
		p.Status, p.Error = "cancelled", ""
	case err != nil:
		p.Status, p.Error = "failed", err.Error()
	default:
		p.Status, p.Completed = "success", p.Total
	}
	result := p.OllamaPull
	st.mu.Unlock()
	p.cancel()

	details := map[string]any{"model": result.Model, "status": result.Status}
	if result.Error != "" {
		details["error"] = result.Error
	}
	if result.Total > 0 {
		details["bytes"] = result.Total
	}
	m.audit.Log(p.auditCtx, audit.ActionOllamaModelPulled, "project", id, details)
	if result.Error != "" {
		m.log.Warn("ollama pull failed", "project", id, "model", result.Model, "error", result.Error)
	}
}

// CancelOllamaPull stops a running download. Ollama keeps what it has fetched so far; the
// next pull of the model continues from there.
func (m *Manager) CancelOllamaPull(_ context.Context, id, model string) error {
	st := m.pulls()
	st.mu.Lock()
	defer st.mu.Unlock()
	p := st.pulls[pullKey(id, model)]
	if p == nil || p.Done {
		return fmt.Errorf("%w: no download of %s is running", ErrNotFound, model)
	}
	p.cancel()
	return nil
}

// DeleteOllamaModel removes a model from the shared store – for every project, which the
// UI says before it asks.
func (m *Manager) DeleteOllamaModel(ctx context.Context, id, model string) error {
	if err := ValidateOllamaModel(model); err != nil {
		return err
	}
	c, err := m.ollamaContainer(ctx, id)
	if err != nil {
		return err
	}
	err = m.ollamaAPI(ctx, c.ID, http.MethodDelete, "/api/delete", map[string]any{"model": model}, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return ollamaError(resp)
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionOllamaModelDeleted, "project", id, map[string]any{"model": model})
	return nil
}

// checkGPU starts a throwaway container from image with the GPUs before a project asks
// for them, so a host without the NVIDIA Container Toolkit is told why instead of being
// left with an Ollama that no longer starts.
func (m *Manager) checkGPU(ctx context.Context, projectID, slug, image string) error {
	if err := m.engine.EnsureImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	res, err := m.engine.RunOneShot(ctx, docker.ContainerSpec{
		Name:       fmt.Sprintf("envoryx-%s-gpucheck-%d", slug, time.Now().UnixNano()%1_000_000),
		Image:      image,
		Labels:     docker.ManagedLabels(projectID, slug, "gpucheck", ""),
		Entrypoint: []string{"true"},
		GPUs:       true,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w (the check container exited with %d)", docker.ErrNoGPU, res.ExitCode)
	}
	return nil
}

// cancelOllamaPulls ends a project's downloads (the project stops or goes away).
func (m *Manager) cancelOllamaPulls(id string) {
	st := m.pulls()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, p := range st.pulls {
		if p.projectID == id && !p.Done {
			p.cancel()
		}
	}
}

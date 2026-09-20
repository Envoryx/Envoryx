package project

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/store"
)

// Operation is a long-running project action as the UI sees it: what runs, on which
// project, which step it is at. Finished operations stay visible for a while so a
// browser that polls sees the outcome even when it did not issue the request.
type Operation struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectSlug string `json:"projectSlug"`
	ProjectName string `json:"projectName"`
	Action      string `json:"action"` // create, start, stop, restart, update, delete, image, backup, restore
	// Step is an English sentence with {{placeholders}} that StepArgs fills in; the UI
	// translates the template like any other interface text.
	Step       string            `json:"step,omitempty"`
	StepArgs   map[string]string `json:"stepArgs,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// Running reports whether the operation is still in progress.
func (o Operation) Running() bool { return o.FinishedAt == nil }

const (
	keepSucceeded = 30 * time.Second
	keepFailed    = 5 * time.Minute
)

// progress tracks the operations of a Manager. One handle lives in the context of every
// detached operation, so deep call sites report steps without passing anything around.
type progress struct {
	mu  sync.Mutex
	ops map[string]*Operation
	now func() time.Time
}

func newProgress() *progress {
	return &progress{ops: map[string]*Operation{}, now: time.Now}
}

type opHandle struct {
	p  *progress
	id string
}

type opKey struct{}

// begin registers a running operation and returns the context that carries it.
func (p *progress) begin(ctx context.Context, op Operation) (context.Context, *opHandle) {
	now := p.now()
	op.ID = store.NewID()
	op.StartedAt, op.UpdatedAt = now, now
	p.mu.Lock()
	p.ops[op.ID] = &op
	p.mu.Unlock()
	h := &opHandle{p: p, id: op.ID}
	return context.WithValue(ctx, opKey{}, h), h
}

func (h *opHandle) update(fn func(*Operation)) {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	if op, ok := h.p.ops[h.id]; ok {
		fn(op)
		op.UpdatedAt = h.p.now()
	}
}

// end records the outcome; the entry expires after keepSucceeded / keepFailed.
func (h *opHandle) end(err error) {
	h.update(func(op *Operation) {
		t := op.UpdatedAt
		op.FinishedAt = &t
		op.Step, op.StepArgs = "", nil
		if err != nil {
			op.Error = err.Error()
		}
	})
}

// step reports the current step of the operation carried by ctx; a no-op outside one.
// kv are placeholder name/value pairs for the template.
func step(ctx context.Context, template string, kv ...string) {
	h, _ := ctx.Value(opKey{}).(*opHandle)
	if h == nil {
		return
	}
	var args map[string]string
	if len(kv) > 0 {
		args = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			args[kv[i]] = kv[i+1]
		}
	}
	h.update(func(op *Operation) { op.Step, op.StepArgs = template, args })
}

// StepText renders the step template with its arguments (logs, tests).
func (o Operation) StepText() string {
	out := o.Step
	for k, v := range o.StepArgs {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// setProject fills in the project id once it exists (create allocates it late).
func setProject(ctx context.Context, id string) {
	h, _ := ctx.Value(opKey{}).(*opHandle)
	if h == nil {
		return
	}
	h.update(func(op *Operation) { op.ProjectID = id })
}

// list returns every known operation, running first, newest first, and drops the
// expired ones on the way.
func (p *progress) list() []Operation {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Operation, 0, len(p.ops))
	for id, op := range p.ops {
		if op.FinishedAt != nil {
			keep := keepSucceeded
			if op.Error != "" {
				keep = keepFailed
			}
			if now.Sub(*op.FinishedAt) > keep {
				delete(p.ops, id)
				continue
			}
		}
		out = append(out, *op)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Running() != out[j].Running() {
			return out[i].Running()
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// active returns the running operation of a project, if any.
func (p *progress) active(projectID string) *Operation {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, op := range p.ops {
		if op.ProjectID == projectID && op.FinishedAt == nil {
			cp := *op
			return &cp
		}
	}
	return nil
}

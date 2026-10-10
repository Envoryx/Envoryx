package plan

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Holder keeps the current plan and picks up changes to its file. The zero Holder and a
// nil *Holder hold no plan.
type Holder struct {
	path string
	log  *slog.Logger

	mu   sync.RWMutex
	plan *Plan
	raw  []byte
	// err is why the file could not be used; the instance then keeps the plan it had.
	err error
	// onChange are called after the plan changed.
	onChange []func(*Plan)
}

// OnChange registers fn to be called with the new plan whenever it changes. Register
// before Run.
func (h *Holder) OnChange(fn func(*Plan)) {
	if h != nil {
		h.onChange = append(h.onChange, fn)
	}
}

// Open loads the plan file at path. A broken file at start is an error: an instance
// that should be limited must not run without its limits.
func Open(path string, log *slog.Logger) (*Holder, error) {
	h := &Holder{path: path, log: log}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(b) > 0 {
		p, err := Parse(b)
		if err != nil {
			return nil, err
		}
		h.plan, h.raw = p, b
	}
	return h, nil
}

// Static returns a Holder with a fixed plan (tests).
func Static(p *Plan) *Holder { return &Holder{plan: p} }

// Get returns the current plan; nil when the instance has none.
func (h *Holder) Get() *Plan {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plan
}

// Err returns why the last change to the file was not taken over.
func (h *Holder) Err() error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

// Reload reads the file again. A removed file removes the plan; a broken one keeps the
// previous plan and is reported by Err.
func (h *Holder) Reload() {
	if h == nil || h.path == "" {
		return
	}
	b, err := os.ReadFile(h.path)
	if err != nil && !os.IsNotExist(err) {
		h.setErr(err)
		return
	}
	h.mu.RLock()
	same := bytes.Equal(b, h.raw) && h.err == nil
	h.mu.RUnlock()
	if same {
		return
	}
	var p *Plan
	if len(b) > 0 {
		if p, err = Parse(b); err != nil {
			h.setErr(err)
			return
		}
	}
	h.mu.Lock()
	h.plan, h.raw, h.err = p, b, nil
	h.mu.Unlock()
	for _, fn := range h.onChange {
		fn(p)
	}
	if h.log != nil {
		if p == nil {
			h.log.Info("plan removed; the instance is no longer limited")
		} else {
			h.log.Info("plan loaded", "name", p.Name)
		}
	}
}

func (h *Holder) setErr(err error) {
	h.mu.Lock()
	changed := h.err == nil || h.err.Error() != err.Error()
	h.err = err
	h.mu.Unlock()
	if changed && h.log != nil {
		h.log.Warn("plan file not taken over; the previous plan stays", "path", h.path, "err", err)
	}
}

// Run checks the file for changes every interval until ctx ends.
func (h *Holder) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.Reload()
		}
	}
}

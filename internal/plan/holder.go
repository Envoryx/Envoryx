package plan

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Holder keeps the current plan and suspension and picks up changes to their files. The
// zero Holder and a nil *Holder hold no plan and no suspension.
type Holder struct {
	path string
	// suspPath is SuspensionFile next to the plan.
	suspPath string
	log      *slog.Logger

	mu   sync.RWMutex
	plan *Plan
	raw  []byte
	// err is why the file could not be used; the instance then keeps the plan it had.
	err error
	// onChange are called after the plan changed.
	onChange []func(*Plan)

	susp    *Suspension
	suspRaw []byte
	// onSuspend are called after the instance was suspended or the suspension lifted.
	onSuspend []func(*Suspension)
}

// OnSuspend registers fn to be called with the new suspension (nil once lifted) whenever
// it changes. Register before Run.
func (h *Holder) OnSuspend(fn func(*Suspension)) {
	if h != nil {
		h.onSuspend = append(h.onSuspend, fn)
	}
}

// OnChange registers fn to be called with the new plan whenever it changes. Register
// before Run.
func (h *Holder) OnChange(fn func(*Plan)) {
	if h != nil {
		h.onChange = append(h.onChange, fn)
	}
}

// Open loads the plan file at path and the suspension next to it. A broken file at start
// is an error: an instance that should be limited must not run without its limits.
func Open(path string, log *slog.Logger) (*Holder, error) {
	h := &Holder{path: path, suspPath: filepath.Join(filepath.Dir(path), SuspensionFile), log: log}
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
	b, err = os.ReadFile(h.suspPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(b) > 0 {
		s, err := ParseSuspension(b)
		if err != nil {
			return nil, err
		}
		h.susp, h.suspRaw = s, b
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

// Suspension returns the hoster's suspension; nil when the instance is not suspended.
func (h *Holder) Suspension() *Suspension {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.susp
}

// SetSuspension suspends the instance or lifts the suspension (tests; the files are what
// a running instance follows).
func (h *Holder) SetSuspension(s *Suspension) {
	h.mu.Lock()
	h.susp = s
	h.mu.Unlock()
	for _, fn := range h.onSuspend {
		fn(s)
	}
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

// Reload reads the files again. A removed file removes the plan or lifts the suspension;
// a broken plan keeps the previous plan and is reported by Err, a broken suspension keeps
// the previous state.
func (h *Holder) Reload() {
	if h == nil || h.path == "" {
		return
	}
	h.reloadSuspension()
	h.reloadPlan()
}

func (h *Holder) reloadPlan() {
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

func (h *Holder) reloadSuspension() {
	b, err := os.ReadFile(h.suspPath)
	if err != nil && !os.IsNotExist(err) {
		if h.log != nil {
			h.log.Warn("suspension file not readable; the previous state stays", "path", h.suspPath, "err", err)
		}
		return
	}
	h.mu.RLock()
	same := bytes.Equal(b, h.suspRaw)
	h.mu.RUnlock()
	if same {
		return
	}
	var s *Suspension
	if len(b) > 0 {
		if s, err = ParseSuspension(b); err != nil {
			// Remembered as seen, so the warning comes once per broken version.
			h.mu.Lock()
			h.suspRaw = b
			h.mu.Unlock()
			if h.log != nil {
				h.log.Warn("suspension file not taken over; the previous state stays", "path", h.suspPath, "err", err)
			}
			return
		}
	}
	h.mu.Lock()
	h.susp, h.suspRaw = s, b
	h.mu.Unlock()
	if h.log != nil {
		if s == nil {
			h.log.Info("the hoster lifted the suspension")
		} else {
			h.log.Warn("the hoster suspended this instance; its projects are stopped", "since", s.Since)
		}
	}
	for _, fn := range h.onSuspend {
		fn(s)
	}
}

// Run checks the files for changes every interval until ctx ends.
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

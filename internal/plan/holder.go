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

	// certPath is CertificateFile next to the plan.
	certPath string
	cert     *Certificate
	certRaw  []byte
	// onCertificate are called after the hoster's certificate came, changed or went.
	onCertificate []func(*Certificate)
}

// OnCertificate registers fn to be called with the hoster's new certificate (nil once it
// is gone) whenever it changes. Register before Run.
func (h *Holder) OnCertificate(fn func(*Certificate)) {
	if h != nil {
		h.onCertificate = append(h.onCertificate, fn)
	}
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
	h := &Holder{path: path, suspPath: filepath.Join(filepath.Dir(path), SuspensionFile), certPath: filepath.Join(filepath.Dir(path), CertificateFile), log: log}
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
	// A broken certificate is only a warning: the instance then falls back to its own
	// certificates, which is no reason to stay down.
	b, err = os.ReadFile(h.certPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	h.certRaw = b
	if len(b) > 0 {
		if c, err := ParseCertificate(b); err != nil {
			if log != nil {
				log.Warn("the hoster's certificate is not used", "path", h.certPath, "err", err)
			}
		} else {
			h.cert = c
		}
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

// Certificate returns the hoster's certificate; nil when the hoster provides none.
func (h *Holder) Certificate() *Certificate {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cert
}

// Set replaces the plan and tells the change handlers (tests; the file is what a running
// instance follows).
func (h *Holder) Set(p *Plan) {
	h.mu.Lock()
	h.plan = p
	h.mu.Unlock()
	for _, fn := range h.onChange {
		fn(p)
	}
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
	h.reloadCertificate()
}

// reloadCertificate picks up a new, changed or removed certificate. A broken one keeps
// the previous one.
func (h *Holder) reloadCertificate() {
	b, err := os.ReadFile(h.certPath)
	if err != nil && !os.IsNotExist(err) {
		if h.log != nil {
			h.log.Warn("the hoster's certificate is not readable; the previous state stays", "path", h.certPath, "err", err)
		}
		return
	}
	h.mu.RLock()
	same := bytes.Equal(b, h.certRaw)
	h.mu.RUnlock()
	if same {
		return
	}
	var c *Certificate
	if len(b) > 0 {
		if c, err = ParseCertificate(b); err != nil {
			h.mu.Lock()
			h.certRaw = b
			h.mu.Unlock()
			if h.log != nil {
				h.log.Warn("the hoster's certificate is not used; the previous state stays", "path", h.certPath, "err", err)
			}
			return
		}
	}
	h.mu.Lock()
	h.cert, h.certRaw = c, b
	h.mu.Unlock()
	if h.log != nil {
		if c == nil {
			h.log.Info("the hoster no longer provides the certificate")
		} else {
			h.log.Info("certificate from the hoster", "domain", c.Domain, "notAfter", c.NotAfter())
		}
	}
	for _, fn := range h.onCertificate {
		fn(c)
	}
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

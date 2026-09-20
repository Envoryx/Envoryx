package project

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Upper bounds for detached operations. They only exist so a hung Docker call cannot
// keep a project locked forever; normal runs finish long before.
const (
	limitProvision = 30 * time.Minute // create, start, restart, update: image pulls
	limitStop      = 5 * time.Minute
	limitDelete    = 10 * time.Minute
	limitBackup    = 2 * time.Hour // dumps and archives of large projects
)

var (
	// ErrShuttingDown is returned for operations requested while Envoryx is stopping.
	ErrShuttingDown = errors.New("Envoryx is shutting down; try again after the restart")
	// ErrInterrupted is the cause recorded on operations that Envoryx had to abandon at
	// shutdown. It ends up in the project's last error, so it tells the user what to do.
	ErrInterrupted = errors.New("interrupted by an Envoryx restart; check the project and run the action again")
	// ErrOperationTimeout is the cause when an operation exceeds its upper bound.
	ErrOperationTimeout = errors.New("the operation took too long and was aborted")
)

// ops tracks the long-running lifecycle operations of a Manager. They are detached from
// the caller's cancellation and drained at shutdown, see Manager.run.
type ops struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing bool
	// root is cancelled (with ErrInterrupted as cause) when the grace period at shutdown
	// runs out; every operation context is tied to it.
	root   context.Context
	cancel context.CancelCauseFunc
}

func newOps() *ops {
	root, cancel := context.WithCancelCause(context.Background())
	return &ops{root: root, cancel: cancel}
}

// run executes a lifecycle operation detached from the caller's cancellation. A browser
// tab closed or a connection lost halfway through an image pull must not abort the
// operation and roll the project back – the caller's context only contributes its values
// (principal, client IP for the audit log). The operation is bounded by limit and ends
// early only when Envoryx shuts down: Shutdown refuses new operations, waits for running
// ones for a grace period and then cancels them with ErrInterrupted as cause.
//
// op names the operation for the UI (action and project); the project name and slug
// are looked up when only the id is known. Steps are reported through step(ctx, …).
func (m *Manager) run(ctx context.Context, limit time.Duration, op Operation, fn func(context.Context) error) error {
	o := m.ops
	o.mu.Lock()
	if o.closing {
		o.mu.Unlock()
		return ErrShuttingDown
	}
	o.wg.Add(1)
	o.mu.Unlock()
	defer o.wg.Done()

	opCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancel(nil)
	opCtx, cancelTimeout := context.WithTimeoutCause(opCtx, limit, ErrOperationTimeout)
	defer cancelTimeout()
	// Propagate the shutdown (and its cause) into this operation.
	stop := context.AfterFunc(o.root, func() { cancel(context.Cause(o.root)) })
	defer stop()

	if op.ProjectID != "" && op.ProjectSlug == "" {
		if p, err := m.store.Projects.Get(opCtx, op.ProjectID); err == nil {
			op.ProjectSlug, op.ProjectName = p.Slug, p.Name
		}
	}
	opCtx, h := m.progress.begin(opCtx, op)
	err := opError(opCtx, fn(opCtx))
	h.end(err)
	return err
}

// runView is run for operations that return the project view: the view is loaded while
// the operation is still registered, so its own entry is stripped before it is returned.
func (m *Manager) runView(ctx context.Context, limit time.Duration, op Operation, fn func(context.Context) (View, error)) (View, error) {
	var view View
	err := m.run(ctx, limit, op, func(ctx context.Context) (err error) {
		view, err = fn(ctx)
		return err
	})
	view.Status.Operation = nil
	return view, err
}

// opError replaces a bare "context canceled"/"deadline exceeded" with the cause the
// operation context was cancelled for (shutdown, time limit), so the message stored on
// the project and returned to the caller explains what happened.
func opError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return err
}

// Shutdown stops accepting lifecycle operations and waits up to grace for the running
// ones to finish; whatever is still running afterwards is cancelled with ErrInterrupted.
// It reports whether everything finished in time.
func (m *Manager) Shutdown(grace time.Duration) bool {
	o := m.ops
	o.mu.Lock()
	o.closing = true
	o.mu.Unlock()

	done := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	finished := true
	select {
	case <-done:
	case <-timer.C:
		finished = false
	}
	o.cancel(ErrInterrupted)
	if !finished {
		<-done
	}
	return finished
}

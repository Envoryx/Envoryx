package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// listTests returns the project's test suites and its newest runs.
func (a *API) listTests(w http.ResponseWriter, r *http.Request) {
	suites, err := a.d.Projects.TestSuites(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	runs, err := a.d.Projects.TestRuns(r.Context(), r.PathValue("id"), 20)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suites": suites, "runs": runs})
}

func (a *API) getTestRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.d.Projects.TestRun(r.Context(), r.PathValue("id"), r.PathValue("run"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

// testWS runs a test suite and streams its output like an action; when it ends the run
// is recorded and sent as {"type":"result","run":…} before the socket closes. The query
// carries filter, cols and rows; {"type":"cancel"} stops the run.
func (a *API) testWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validate.UUID(id); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	s, err := a.d.Projects.RunTests(r.Context(), id, r.PathValue("suite"), r.URL.Query().Get("filter"), sizeParam(r, "cols", 120), sizeParam(r, "rows", 40))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer s.Release()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.d.AllowedOriginHosts})
	if err != nil {
		_ = s.Term.Close()
		return
	}
	defer conn.CloseNow()
	defer s.Term.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	_ = writeWS(ctx, conn, map[string]any{"type": "start", "suite": s.Suite.ID, "cmd": s.Argv()})

	var cancelled atomic.Bool
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText && string(data) == `{"type":"cancel"}` {
				cancelled.Store(true)
				_, _ = s.Term.Input().Write([]byte{0x03})
			}
		}
	}()

	var output []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := s.Term.Output().Read(buf)
		if n > 0 {
			output = append(output, buf[:n]...)
			if len(output) > 256<<10 {
				output = output[len(output)-128<<10:]
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
			wcancel()
			if werr != nil {
				cancelled.Store(true)
				_, _ = s.Term.Input().Write([]byte{0x03})
			}
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			// The browser went away: stop the run, it is recorded as cancelled.
			cancelled.Store(true)
			_, _ = s.Term.Input().Write([]byte{0x03})
		}
		code := -1
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			c, cerr := s.Term.ExitCode(context.WithoutCancel(ctx))
			if cerr == nil && c >= 0 {
				code = c
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !errors.Is(err, io.EOF) && ctx.Err() == nil {
			_ = writeWS(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
		}
		run, ferr := a.d.Projects.FinishTestRun(r.Context(), s, code, cancelled.Load(), output)
		if ferr != nil {
			_ = writeWS(ctx, conn, map[string]any{"type": "error", "message": ferr.Error()})
		}
		_ = writeWS(ctx, conn, map[string]any{"type": "exit", "code": code})
		if ferr == nil {
			_ = writeWS(ctx, conn, map[string]any{"type": "result", "run": run})
		}
		conn.Close(websocket.StatusNormalClosure, "finished")
		return
	}
}

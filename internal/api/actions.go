package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func (a *API) listActions(w http.ResponseWriter, r *http.Request) {
	actions, err := a.d.Projects.ListActions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": actions})
}

// actionWS runs a catalogue action and streams its output. Output-only: the browser can
// send {"type":"cancel"} which delivers Ctrl+C to the process; closing the socket does the
// same. The project lock is held for the duration so lifecycle operations wait.
func (a *API) actionWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validate.UUID(id); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	term, action, release, err := a.d.Projects.RunAction(r.Context(), id, r.PathValue("action"), sizeParam(r, "cols", 120), sizeParam(r, "rows", 40))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer release()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.d.AllowedOriginHosts})
	if err != nil {
		_ = term.Close()
		return
	}
	defer conn.CloseNow()
	defer term.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	_ = writeWS(ctx, conn, map[string]any{"type": "start", "action": action.ID, "cmd": action.Cmd})

	// Control messages from the browser (cancel). Reading also detects a closed socket.
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText && string(data) == `{"type":"cancel"}` {
				_, _ = term.Input().Write([]byte{0x03})
			}
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := term.Output().Read(buf)
		if n > 0 {
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
			wcancel()
			if werr != nil {
				_, _ = term.Input().Write([]byte{0x03})
				return
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				// Client went away: stop the process.
				_, _ = term.Input().Write([]byte{0x03})
				return
			}
			code := -1
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				c, cerr := term.ExitCode(ctx)
				if cerr == nil && c >= 0 {
					code = c
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !errors.Is(err, io.EOF) {
				_ = writeWS(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
			}
			_ = writeWS(ctx, conn, map[string]any{"type": "exit", "code": code})
			a.d.Audit.Log(r.Context(), "action.finished", "project", id, map[string]any{"action": action.ID, "exitCode": code})
			conn.Close(websocket.StatusNormalClosure, "finished")
			return
		}
	}
}

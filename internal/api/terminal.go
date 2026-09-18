package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func sizeParam(r *http.Request, key string, def uint) uint {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n <= 0 || n > 1000 {
		return def
	}
	return uint(n)
}

// terminalWS bridges a browser terminal (xterm.js) to a PTY exec session in a project
// container. Binary frames carry keystrokes/output, text frames carry control messages
// ({"type":"resize","cols":..,"rows":..}).
func (a *API) terminalWS(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	if err := validate.UUID(id); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	// Open the session before upgrading so failures are ordinary HTTP errors.
	term, err := a.d.Projects.OpenTerminal(r.Context(), id, kind, sizeParam(r, "cols", 120), sizeParam(r, "rows", 40))
	if err != nil {
		writeError(w, r, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.d.AllowedOriginHosts})
	if err != nil {
		_ = term.Close()
		a.d.Log.Debug("websocket accept failed", "err", err)
		return
	}
	defer conn.CloseNow()
	defer term.Close()
	conn.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Container → browser.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := term.Output().Read(buf)
			if n > 0 {
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
				wcancel()
				if werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					_ = writeWS(ctx, conn, map[string]any{"type": "error", "message": "session ended: " + err.Error()})
				}
				_ = writeWS(ctx, conn, map[string]any{"type": "exit"})
				conn.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
		}
	}()

	// Browser → container.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := term.Input().Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var msg struct {
				Type string `json:"type"`
				Cols uint   `json:"cols"`
				Rows uint   `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 && msg.Cols <= 1000 && msg.Rows <= 1000 {
				_ = term.Resize(ctx, msg.Cols, msg.Rows)
			}
		}
	}
}

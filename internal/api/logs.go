package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func serviceKind(r *http.Request) (store.ServiceKind, error) {
	switch k := store.ServiceKind(r.PathValue("kind")); k {
	case store.ServicePHP, store.ServiceWeb, store.ServiceDatabase, store.ServiceNode, store.ServiceRedis, store.ServiceMailpit, store.ServiceStorage:
		return k, nil
	default:
		if wid, ok := strings.CutPrefix(string(k), "worker:"); ok && validate.UUID(wid) == nil {
			return k, nil
		}
		return "", newError(http.StatusNotFound, "not_found", "unknown service")
	}
}

func tailParam(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("tail"))
	if err != nil || n <= 0 {
		return def
	}
	if n > 10000 {
		return 10000
	}
	return n
}

// serviceLogs returns the last lines of a service (also used for downloads).
func (a *API) serviceLogs(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	lines, err := a.d.Projects.TailLogs(r.Context(), r.PathValue("id"), kind, tailParam(r, 500))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if lines == nil {
		lines = []docker.LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// serviceLogsWS streams logs over a WebSocket. Authentication happened in the session
// middleware (cookie); the library rejects cross-origin upgrades unless the origin is on
// the allow-list (dev server only).
func (a *API) serviceLogsWS(w http.ResponseWriter, r *http.Request) {
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
	// Resolve before upgrading so errors arrive as ordinary HTTP responses.
	if _, err := a.d.Projects.ServiceContainer(r.Context(), id, kind); err != nil {
		writeError(w, r, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.d.AllowedOriginHosts})
	if err != nil {
		a.d.Log.Debug("websocket accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	// The client only sends control frames; CloseRead cancels ctx when it goes away.
	ctx := conn.CloseRead(r.Context())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan docker.LogLine, 512)
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.d.Projects.StreamLogs(ctx, id, kind, docker.LogOptions{Tail: strconv.Itoa(tailParam(r, 200)), Follow: true}, func(l docker.LogLine) {
			select {
			case lines <- l:
			case <-ctx.Done():
			}
		})
		close(lines)
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		case l, ok := <-lines:
			if !ok {
				err := <-errCh
				if err != nil && !errors.Is(err, context.Canceled) {
					_ = writeWS(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
					conn.Close(websocket.StatusInternalError, "log stream failed")
					return
				}
				conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			if err := writeWS(ctx, conn, map[string]any{"type": "line", "time": l.Time, "stream": l.Stream, "text": l.Text}); err != nil {
				return
			}
		}
	}
}

func writeWS(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func serviceKind(r *http.Request) (store.ServiceKind, error) {
	switch k := store.ServiceKind(r.PathValue("kind")); k {
	case store.ServicePHP, store.ServiceWeb, store.ServiceDatabase, store.ServicePython, store.ServiceGo, store.ServiceNode, store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama, store.ServiceStorage:
		return k, nil
	default:
		if wid, ok := strings.CutPrefix(string(k), "worker:"); ok && validate.UUID(wid) == nil {
			return k, nil
		}
		if name := k.DatabaseName(); name != "" && project.ValidateDatabaseServiceName(name) == nil {
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

// logQuery reads the filter parameters shared by the log endpoints: since and until
// (RFC 3339 or a duration back from now such as 6h or 7d), q (text), level (warn, error)
// and stream (stdout, stderr).
func logQuery(r *http.Request) (logs.Query, error) {
	v := r.URL.Query()
	now := time.Now()
	since, err := logs.ParseTime(v.Get("since"), now)
	if err != nil {
		return logs.Query{}, fmt.Errorf("%w: since: %v", validate.ErrInvalid, err)
	}
	until, err := logs.ParseTime(v.Get("until"), now)
	if err != nil {
		return logs.Query{}, fmt.Errorf("%w: until: %v", validate.ErrInvalid, err)
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return logs.Query{}, fmt.Errorf("%w: until is before since", validate.ErrInvalid)
	}
	level, ok := logs.ParseLevel(v.Get("level"))
	if !ok {
		return logs.Query{}, fmt.Errorf("%w: level must be warn or error", validate.ErrInvalid)
	}
	stream := v.Get("stream")
	if stream != "" && stream != "stdout" && stream != "stderr" {
		return logs.Query{}, fmt.Errorf("%w: stream must be stdout or stderr", validate.ErrInvalid)
	}
	return logs.Query{Since: since, Until: until, Text: v.Get("q"), MinLevel: level, Stream: stream}, nil
}

// serviceLogs returns the last lines of a service that match the filter.
func (a *API) serviceLogs(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q, err := logQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := a.d.Projects.QueryLogs(r.Context(), r.PathValue("id"), kind, q, tailParam(r, 500))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// serviceLogsStats returns the line, warning and error counts over time and the most
// frequent problems.
func (a *API) serviceLogsStats(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q, err := logQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sum, err := a.d.Projects.LogStats(r.Context(), r.PathValue("id"), kind, q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// serviceLogsDownload streams every matching line as a file: plain text
// ("<time> [stream] text") or, with format=jsonl, one JSON object per line.
func (a *API) serviceLogsDownload(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q, err := logQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	jsonl := r.URL.Query().Get("format") == "jsonl"
	id := r.PathValue("id")
	if err := validate.UUID(id); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	p, err := a.d.Store.Projects.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// Resolve the source before the first byte, so a missing container is still an
	// ordinary error response.
	var bw *bufio.Writer
	var enc *json.Encoder
	started := false
	start := func() {
		started = true
		ext, ctype := "log", "text/plain; charset=utf-8"
		if jsonl {
			ext, ctype = "jsonl", "application/x-ndjson"
		}
		name := fmt.Sprintf("envoryx-%s-%s-%s.%s", p.Slug, strings.ReplaceAll(string(kind), ":", "-"), time.Now().UTC().Format("20060102-150405"), ext)
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		bw = bufio.NewWriterSize(w, 64<<10)
		enc = json.NewEncoder(bw)
	}
	err = a.d.Projects.ExportLogs(r.Context(), id, kind, q, func(l logs.Line) {
		if !started {
			start()
		}
		if jsonl {
			_ = enc.Encode(l)
			return
		}
		ts := ""
		if !l.Time.IsZero() {
			ts = l.Time.UTC().Format(time.RFC3339Nano) + " "
		}
		_, _ = bw.WriteString(ts + "[" + l.Stream + "] " + l.Text + "\n")
	})
	if err != nil && !started {
		writeError(w, r, err)
		return
	}
	if !started {
		start()
	}
	if err != nil {
		// Headers are gone; say so at the end of the file rather than cutting it silently.
		_, _ = bw.WriteString("\n# envoryx: export aborted: " + err.Error() + "\n")
	}
	_ = bw.Flush()
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
	// The filter applies to the live lines too; tail counts before it.
	q, err := logQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	match := logs.NewMatcher(q)
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

	lines := make(chan logs.Line, 512)
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.d.Projects.StreamLogs(ctx, id, kind, docker.LogOptions{Tail: strconv.Itoa(tailParam(r, 200)), Since: q.Since, Follow: true}, func(l docker.LogLine) {
			lvl, ok := match.Match(l)
			if !ok {
				return
			}
			select {
			case lines <- logs.NewLine(l, lvl):
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
			if err := writeWS(ctx, conn, map[string]any{"type": "line", "time": l.Time, "stream": l.Stream, "text": l.Text, "level": l.Level}); err != nil {
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

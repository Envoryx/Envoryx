// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/validate"
)

// execRequest runs one command in a service container. Cmd is argv – nothing is handed
// to a shell, so a caller who wants pipes or redirection asks for one explicitly
// ("sh", "-lc", "…"). Stdin is the command's input; it shares the 1 MiB body limit, and
// anything larger belongs in SSH (`ssh <slug>@host "…" < big-file`) or a backup.
type execRequest struct {
	Cmd   []string `json:"cmd"`
	Stdin string   `json:"stdin"`
}

const (
	maxExecArgs   = 64
	maxExecArgLen = 4096
)

// exec runs a command and streams its output as newline-delimited JSON objects:
//
//	{"type":"stdout","text":"…"}
//	{"type":"stderr","text":"…"}
//	{"type":"exit","code":0}
//
// A failure before the first byte is an ordinary HTTP error; once output is flowing the
// stream ends with {"type":"error","message":"…"} instead of an exit code. The streams
// stay apart and the exit code survives – unlike the terminal WebSocket, which is a
// pseudo-terminal for humans.
func (a *API) exec(w http.ResponseWriter, r *http.Request) {
	kind, err := serviceKind(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req execRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := validateExecCmd(req.Cmd); err != nil {
		writeError(w, r, err)
		return
	}

	// The status line is left for the first frame to trigger: as long as nothing has
	// been written, a refusal (no such service, container not running) can still travel
	// as an ordinary HTTP error instead of a frame nobody checks for.
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	stream := newExecStream(w)

	opts := project.ExecOptions{
		Cmd:    req.Cmd,
		Stdout: stream.writer("stdout"),
		Stderr: stream.writer("stderr"),
	}
	if req.Stdin != "" {
		opts.Stdin = strings.NewReader(req.Stdin)
	}
	code, err := a.d.Projects.Exec(r.Context(), r.PathValue("id"), kind, opts)
	stream.flushPartial()
	if err != nil {
		if !stream.started() {
			writeError(w, r, err)
			return
		}
		// Output has already gone out, so the reason travels in the stream, where the
		// client reads it instead of an exit code.
		stream.send(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	stream.send(map[string]any{"type": "exit", "code": code})
}

func validateExecCmd(cmd []string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("%w: cmd must name a command", validate.ErrInvalid)
	}
	if len(cmd) > maxExecArgs {
		return fmt.Errorf("%w: at most %d arguments", validate.ErrInvalid, maxExecArgs)
	}
	if strings.TrimSpace(cmd[0]) == "" {
		return fmt.Errorf("%w: cmd must name a command", validate.ErrInvalid)
	}
	for _, arg := range cmd {
		if len(arg) > maxExecArgLen {
			return fmt.Errorf("%w: argument longer than %d bytes", validate.ErrInvalid, maxExecArgLen)
		}
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("%w: arguments must not contain NUL", validate.ErrInvalid)
		}
	}
	return nil
}

// execStream writes the NDJSON frames of one exec, flushing each so output appears while
// the command is still running.
type execStream struct {
	mu      sync.Mutex
	enc     *json.Encoder
	rc      *http.ResponseController
	writers []*execStreamWriter
	sent    bool
	dead    bool
}

// started reports whether a frame has gone out, and with it the 200 status line.
func (s *execStream) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func newExecStream(w http.ResponseWriter) *execStream {
	return &execStream{enc: json.NewEncoder(w), rc: http.NewResponseController(w)}
}

func (s *execStream) send(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(v)
}

func (s *execStream) write(v any) {
	if s.dead {
		return
	}
	if err := s.enc.Encode(v); err != nil {
		s.dead = true
		return
	}
	s.sent = true
	// Not every ResponseWriter can flush (tests); output then arrives in one piece.
	_ = s.rc.Flush()
}

func (s *execStream) writer(name string) *execStreamWriter {
	w := &execStreamWriter{stream: s, name: name}
	s.writers = append(s.writers, w)
	return w
}

// flushPartial emits bytes that were held back as the start of a rune whose rest never
// came – a command that ends mid-character or writes binary. Dropping them silently
// would be worse than the replacement characters JSON puts in their place.
func (s *execStream) flushPartial() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.writers {
		if len(w.rest) > 0 {
			s.write(map[string]any{"type": w.name, "text": string(w.rest)})
			w.rest = nil
		}
	}
}

// execStreamWriter turns container output into frames. Docker chops the output into
// arbitrary chunks, so a multi-byte character can straddle two of them; the trailing
// bytes of an incomplete rune wait for the next write rather than becoming U+FFFD.
type execStreamWriter struct {
	stream *execStream
	name   string
	rest   []byte
}

func (w *execStreamWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	buf := p
	if len(w.rest) > 0 {
		buf = append(w.rest, p...)
		w.rest = nil
	}
	if cut := completeRunes(buf); cut < len(buf) {
		w.rest = append([]byte(nil), buf[cut:]...)
		buf = buf[:cut]
	}
	if len(buf) > 0 {
		w.stream.write(map[string]any{"type": w.name, "text": string(buf)})
	}
	return n, nil
}

// completeRunes returns the length of b without a trailing UTF-8 sequence whose
// remaining bytes have not arrived yet.
func completeRunes(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue // continuation byte: keep walking back to the start of the rune
		}
		if need := runeLen(b[i]); need > len(b)-i {
			return i
		}
		return len(b)
	}
	return len(b)
}

// runeLen is the number of bytes the rune starting with c occupies; invalid lead bytes
// count as one so they are passed on instead of being waited for.
func runeLen(c byte) int {
	switch {
	case c < 0x80:
		return 1
	case c&0xE0 == 0xC0:
		return 2
	case c&0xF0 == 0xE0:
		return 3
	case c&0xF8 == 0xF0:
		return 4
	}
	return 1
}

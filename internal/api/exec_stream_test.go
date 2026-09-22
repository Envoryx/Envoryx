package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Docker hands output over in arbitrary chunks, so a multi-byte character can straddle
// two of them. The frames must still carry the text, not replacement characters.
func TestExecStreamKeepsMultiByteOutputIntact(t *testing.T) {
	const text = "Größe: 20 µF – ✓"
	for _, chunk := range []int{1, 2, 3, 5} {
		rec := httptest.NewRecorder()
		stream := newExecStream(rec)
		w := stream.writer("stdout")
		raw := []byte(text)
		for i := 0; i < len(raw); i += chunk {
			end := min(i+chunk, len(raw))
			if n, err := w.Write(raw[i:end]); err != nil || n != end-i {
				t.Fatalf("write: %d %v", n, err)
			}
		}
		stream.flushPartial()
		stream.send(map[string]any{"type": "exit", "code": 0})

		var out strings.Builder
		var frames int
		for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
			var frame struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				t.Fatalf("frame %q: %v", line, err)
			}
			if frame.Type == "stdout" {
				out.WriteString(frame.Text)
				frames++
			}
		}
		if out.String() != text {
			t.Fatalf("chunk %d: output = %q", chunk, out.String())
		}
		if frames == 0 {
			t.Fatalf("chunk %d: no output frames", chunk)
		}
	}
}

// Bytes that never became a whole character are still reported rather than swallowed.
func TestExecStreamFlushesUnfinishedBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := newExecStream(rec)
	w := stream.writer("stderr")
	if _, err := w.Write([]byte{'a', 0xC3}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "\\ufffd") {
		t.Fatalf("partial rune was emitted early: %s", rec.Body.String())
	}
	stream.flushPartial()
	if lines := strings.Count(strings.TrimSpace(rec.Body.String()), "\n"); lines != 1 {
		t.Fatalf("expected two frames, got %s", rec.Body.String())
	}
}

func TestValidateExecCmd(t *testing.T) {
	if err := validateExecCmd([]string{"ls", "-la"}); err != nil {
		t.Fatalf("plain command: %v", err)
	}
	for _, cmd := range [][]string{
		nil,
		{"  "},
		make([]string, maxExecArgs+1),
		{strings.Repeat("x", maxExecArgLen+1)},
		{"sh", "-c", "echo\x00"},
	} {
		if err := validateExecCmd(cmd); err == nil {
			t.Fatalf("accepted %q", cmd)
		}
	}
}

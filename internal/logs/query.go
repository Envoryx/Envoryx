package logs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// Query selects lines from a service's output.
type Query struct {
	// Since and Until bound the time range (zero = open, both inclusive).
	Since, Until time.Time
	// Text is a case-insensitive substring every returned line contains.
	Text string
	// MinLevel drops lines below this level.
	MinLevel Level
	// Stream is "stdout", "stderr" or "" for both.
	Stream string
}

// Filtered reports whether the query looks at the text, so a source cannot hand the
// line count to Docker's tail.
func (q Query) Filtered() bool {
	return q.Text != "" || q.MinLevel > LevelInfo || q.Stream != ""
}

// Matcher tests lines against a query; it lower-cases the search text once.
type Matcher struct {
	q    Query
	text string
}

// NewMatcher prepares q for matching.
func NewMatcher(q Query) *Matcher {
	return &Matcher{q: q, text: strings.ToLower(q.Text)}
}

// Match reports whether the line belongs to the result and returns its level.
func (m *Matcher) Match(l docker.LogLine) (Level, bool) {
	if !m.q.Since.IsZero() && !l.Time.IsZero() && l.Time.Before(m.q.Since) {
		return 0, false
	}
	if !m.q.Until.IsZero() && l.Time.After(m.q.Until) {
		return 0, false
	}
	if m.q.Stream != "" && l.Stream != m.q.Stream {
		return 0, false
	}
	if m.text != "" && !strings.Contains(strings.ToLower(l.Text), m.text) {
		return 0, false
	}
	lvl := Classify(l.Text)
	return lvl, lvl >= m.q.MinLevel
}

// ErrBadTime is returned by ParseTime for values it does not understand.
var ErrBadTime = errors.New("time must be RFC 3339 (2026-09-24T10:00:00Z) or a duration before now (90m, 6h, 7d)")

// ParseTime reads a point in time for the API and the CLI: an RFC 3339 timestamp, or a
// duration counted back from now ("15m", "6h", "7d"). "" is the zero time.
func ParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%w: %q", ErrBadTime, s)
}

// Ring keeps the last n lines appended to it.
type Ring struct {
	n     int
	lines []Line
	start int
	// Total counts every line offered, including the ones that fell out.
	Total int
}

// Line is a log line with its level, as the API returns it.
type Line struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
	Level  string    `json:"level,omitempty"`
}

// NewLine attaches the level to a line.
func NewLine(l docker.LogLine, lvl Level) Line {
	return Line{Time: l.Time, Stream: l.Stream, Text: l.Text, Level: lvl.String()}
}

// NewRing returns a ring for the last n lines.
func NewRing(n int) *Ring { return &Ring{n: n} }

// Add appends a line, dropping the oldest when the ring is full.
func (r *Ring) Add(l Line) {
	r.Total++
	if len(r.lines) < r.n {
		r.lines = append(r.lines, l)
		return
	}
	r.lines[r.start] = l
	r.start = (r.start + 1) % r.n
}

// Lines returns the kept lines, oldest first.
func (r *Ring) Lines() []Line {
	out := make([]Line, 0, len(r.lines))
	out = append(out, r.lines[r.start:]...)
	return append(out, r.lines[:r.start]...)
}

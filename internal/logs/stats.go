package logs

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Bucket counts the lines of one time slot.
type Bucket struct {
	Start    time.Time `json:"start"`
	Total    int       `json:"total"`
	Warnings int       `json:"warnings"`
	Errors   int       `json:"errors"`
}

// Message is a recurring error or warning, grouped by its text with the variable parts
// (numbers, ids, times) masked.
type Message struct {
	Level   string    `json:"level"`
	Pattern string    `json:"pattern"`
	Example string    `json:"example"`
	Count   int       `json:"count"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// Summary is the error frequency of a time range.
type Summary struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int       `json:"bucketSeconds"`
	Buckets       []Bucket  `json:"buckets"`
	Total         int       `json:"total"`
	Warnings      int       `json:"warnings"`
	Errors        int       `json:"errors"`
	Top           []Message `json:"top"`
}

// Stats accumulates lines for a Summary. Counting happens per minute; the bucket size
// is chosen at the end, when the range is known.
type Stats struct {
	minutes  map[int64]*Bucket
	messages map[string]*Message
	first    time.Time
	last     time.Time
}

// maxPatterns bounds the grouping map when a service logs endless distinct errors.
const maxPatterns = 2000

// NewStats returns an empty accumulator.
func NewStats() *Stats {
	return &Stats{minutes: map[int64]*Bucket{}, messages: map[string]*Message{}}
}

// Add counts one line.
func (s *Stats) Add(l Line, lvl Level) {
	if l.Time.IsZero() {
		return
	}
	if s.first.IsZero() || l.Time.Before(s.first) {
		s.first = l.Time
	}
	if l.Time.After(s.last) {
		s.last = l.Time
	}
	key := l.Time.Unix() / 60
	b := s.minutes[key]
	if b == nil {
		b = &Bucket{Start: time.Unix(key*60, 0).UTC()}
		s.minutes[key] = b
	}
	b.Total++
	switch lvl {
	case LevelWarn:
		b.Warnings++
	case LevelError:
		b.Errors++
	default:
		return
	}
	pattern := Pattern(l.Text)
	m := s.messages[pattern]
	if m == nil {
		if len(s.messages) >= maxPatterns {
			return
		}
		m = &Message{Level: lvl.String(), Pattern: pattern, Example: clip(l.Text, 500), First: l.Time}
		s.messages[pattern] = m
	}
	m.Count++
	if l.Time.After(m.Last) {
		m.Last = l.Time
		m.Example = clip(l.Text, 500)
	}
}

// bucketSizes are the slot widths a chart may use, smallest first.
var bucketSizes = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// maxBuckets is roughly how many bars fit a chart.
const maxBuckets = 96

// Summary returns the counts for [from, to]; a zero bound is taken from the lines.
func (s *Stats) Summary(from, to time.Time, top int) Summary {
	if from.IsZero() {
		from = s.first
	}
	if to.IsZero() {
		to = s.last
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		to = from.Add(time.Minute)
	}
	size := bucketSizes[len(bucketSizes)-1]
	for _, d := range bucketSizes {
		if to.Sub(from)/d < maxBuckets {
			size = d
			break
		}
	}
	start := from.Truncate(size)
	n := int(to.Sub(start)/size) + 1
	out := Summary{From: from, To: to, BucketSeconds: int(size / time.Second), Buckets: make([]Bucket, n), Top: []Message{}}
	for i := range out.Buckets {
		out.Buckets[i].Start = start.Add(time.Duration(i) * size).UTC()
	}
	for _, b := range s.minutes {
		i := int(b.Start.Sub(start) / size)
		if i < 0 || i >= n {
			continue
		}
		out.Buckets[i].Total += b.Total
		out.Buckets[i].Warnings += b.Warnings
		out.Buckets[i].Errors += b.Errors
		out.Total += b.Total
		out.Warnings += b.Warnings
		out.Errors += b.Errors
	}
	for _, m := range s.messages {
		out.Top = append(out.Top, *m)
	}
	sort.Slice(out.Top, func(i, j int) bool {
		a, b := out.Top[i], out.Top[j]
		if a.Level != b.Level {
			return a.Level == "error"
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Last.After(b.Last)
	})
	if len(out.Top) > top {
		out.Top = out.Top[:top]
	}
	return out
}

var (
	patternTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?|\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2}(?: [+-]\d{4})?|\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`)
	patternUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	patternHex  = regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b|\b[0-9a-f]{12,}\b`)
	patternNum  = regexp.MustCompile(`\d+`)
)

// Pattern masks the parts of a line that differ between occurrences of the same
// problem, so they group: times, ids, addresses and numbers.
func Pattern(text string) string {
	p := clip(strings.TrimSpace(text), 300)
	p = patternTime.ReplaceAllString(p, "…")
	p = patternUUID.ReplaceAllString(p, "…")
	p = patternHex.ReplaceAllString(p, "…")
	return patternNum.ReplaceAllString(p, "#")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s + "…"
}

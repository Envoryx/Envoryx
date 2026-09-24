package logs

import (
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

func TestClassify(t *testing.T) {
	for text, want := range map[string]Level{
		"NOTICE: fpm is running, pid 1":                                             LevelInfo,
		"PHP Fatal error:  Uncaught Error: Call to undefined function foo()":        LevelError,
		"PHP Warning:  Undefined variable $x in /app/index.php on line 3":           LevelWarn,
		"PHP Deprecated:  Creation of dynamic property":                             LevelWarn,
		`2026/09/24 10:00:00 [error] 29#29: *1 open() "/app/x" failed`:              LevelError,
		`2026/09/24 10:00:00 [crit] 29#29: *1 connect() failed`:                     LevelError,
		"2026-09-24 10:00:00.123 UTC [1] ERROR:  relation \"users\" does not exist": LevelError,
		"2026-09-24T10:00:00.000000Z 0 [Warning] [MY-010068] CA certificate":        LevelWarn,
		"Traceback (most recent call last):":                                        LevelError,
		`172.18.0.1 - - [24/Sep/2026:10:00:00 +0000] "GET / HTTP/1.1" 502 157`:      LevelError,
		`172.18.0.1 - - [24/Sep/2026:10:00:00 +0000] "GET / HTTP/1.1" 404 157`:      LevelInfo,
		"Tests: 12 passed, 0 errors":                                                LevelInfo,
		"php_value[error_log] = /proc/self/fd/2":                                    LevelInfo,
		`level=info msg="request failed" err=<nil>`:                                 LevelInfo,
		`level=error msg="db down"`:                                                 LevelError,
		`{"level":"info","msg":"an error page was rendered"}`:                       LevelInfo,
		`{"level":"error","msg":"handler failed"}`:                                  LevelError,
		`{"level":50,"msg":"pino error"}`:                                           LevelError,
		`{"severity":"WARNING","message":"slow"}`:                                   LevelWarn,
	} {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":                     {},
		"90m":                  now.Add(-90 * time.Minute),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"2026-09-24T10:00:00Z": time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
	} {
		got, err := ParseTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "-5m", "3x"} {
		if _, err := ParseTime(bad, now); err == nil {
			t.Errorf("ParseTime(%q) must fail", bad)
		}
	}
}

func TestMatcherAndRing(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	m := NewMatcher(Query{Since: base.Add(time.Minute), Until: base.Add(3 * time.Minute), Text: "USER", MinLevel: LevelWarn})
	ring := NewRing(2)
	for i, text := range []string{"user error early", "user error", "Warning: user", "info user", "user error late"} {
		l := docker.LogLine{Time: base.Add(time.Duration(i) * time.Minute), Stream: "stderr", Text: text}
		if lvl, ok := m.Match(l); ok {
			ring.Add(NewLine(l, lvl))
		}
	}
	got := ring.Lines()
	if ring.Total != 2 || len(got) != 2 || got[0].Text != "user error" || got[1].Text != "Warning: user" || got[1].Level != "warn" {
		t.Fatalf("ring: total=%d %+v", ring.Total, got)
	}
	ring = NewRing(2)
	for _, s := range []string{"a", "b", "c"} {
		ring.Add(Line{Text: s})
	}
	if got := ring.Lines(); got[0].Text != "b" || got[1].Text != "c" || ring.Total != 3 {
		t.Fatalf("wrapped ring: %+v", got)
	}
}

func TestStatsSummary(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	s := NewStats()
	add := func(offset time.Duration, text string) {
		lvl := Classify(text)
		s.Add(Line{Time: base.Add(offset), Text: text}, lvl)
	}
	add(0, "GET / 200")
	add(10*time.Second, "PHP Fatal error: Out of memory (allocated 2097152) at 10:00:10")
	add(2*time.Hour, "PHP Fatal error: Out of memory (allocated 4194304) at 12:00:00")
	add(3*time.Hour, "PHP Warning: slow")
	sum := s.Summary(base, base.Add(6*time.Hour), 10)
	if sum.BucketSeconds != 300 || len(sum.Buckets) != 73 {
		t.Fatalf("buckets: %d × %ds", len(sum.Buckets), sum.BucketSeconds)
	}
	if sum.Total != 4 || sum.Errors != 2 || sum.Warnings != 1 || sum.Buckets[0].Total != 2 || sum.Buckets[0].Errors != 1 || sum.Buckets[24].Errors != 1 {
		t.Fatalf("counts: %+v", sum)
	}
	if len(sum.Top) != 2 || sum.Top[0].Count != 2 || sum.Top[0].Pattern != "PHP Fatal error: Out of memory (allocated #) at …" || sum.Top[1].Level != "warn" {
		t.Fatalf("top: %+v", sum.Top)
	}
	if !sum.Top[0].First.Equal(base.Add(10*time.Second)) || !sum.Top[0].Last.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("first/last: %+v", sum.Top[0])
	}
	// Without bounds the range comes from the lines.
	if sum := s.Summary(time.Time{}, time.Time{}, 1); !sum.From.Equal(base) || len(sum.Top) != 1 {
		t.Fatalf("open range: %+v", sum)
	}
}

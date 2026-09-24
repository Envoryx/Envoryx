package logs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

const testProject = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

func texts(lines []docker.LogLine) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return strings.Join(out, ",")
}

func scanAll(t *testing.T, s *Store, k Key, since, until time.Time) []docker.LogLine {
	t.Helper()
	var got []docker.LogLine
	if err := s.Scan(context.Background(), k, since, until, func(l docker.LogLine) { got = append(got, l) }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStoreAppendScanTail(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := Key{Project: testProject, Service: "php"}
	day1 := time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)
	lines := []docker.LogLine{
		{Time: day1, Stream: "stderr", Text: "a"},
		{Time: day1.Add(30 * time.Second), Stream: "stdout", Text: `b "quoted" <html>`},
		{Time: day1.Add(2 * time.Minute), Stream: "stdout", Text: "c"}, // next day
		{Time: day1.Add(25 * time.Hour), Stream: "stderr", Text: "d"},
	}
	if err := s.Append(k, lines); err != nil {
		t.Fatal(err)
	}
	if files, _ := os.ReadDir(filepath.Join(s.Dir(), testProject, "php")); len(files) != 3 {
		t.Fatalf("one file per day: %v", files)
	}
	got := scanAll(t, s, k, time.Time{}, time.Time{})
	if texts(got) != `a,b "quoted" <html>,c,d` || got[0].Stream != "stderr" || got[1].Stream != "stdout" || !got[3].Time.Equal(lines[3].Time) {
		t.Fatalf("scan: %+v", got)
	}
	if got := scanAll(t, s, k, day1.Add(time.Minute), day1.Add(3*time.Minute)); texts(got) != "c" {
		t.Fatalf("range: %s", texts(got))
	}
	if last := s.Last(k); !last.Equal(lines[3].Time) {
		t.Fatalf("last: %v", last)
	}
	// A fresh store finds the newest line on disk.
	s2, _ := OpenStore(s.Dir())
	if last := s2.Last(k); !last.Equal(lines[3].Time) {
		t.Fatalf("last from disk: %v", last)
	}
	if oldest := s2.Oldest(context.Background(), k); !oldest.Equal(day1) {
		t.Fatalf("oldest: %v", oldest)
	}

	tail, total, err := s.Tail(context.Background(), k, time.Time{}, 2)
	if err != nil || texts(tail) != "c,d" || total != 4 {
		t.Fatalf("tail: %s %d %v", texts(tail), total, err)
	}
	tail, total, _ = s.Tail(context.Background(), k, day1.Add(time.Minute), 5)
	if texts(tail) != `a,b "quoted" <html>` || total != 2 {
		t.Fatalf("tail until: %s %d", texts(tail), total)
	}

	if s.Has(Key{Project: testProject, Service: "web"}) || !s.Has(k) {
		t.Fatal("has")
	}
	for _, bad := range []Key{{Project: "../etc", Service: "php"}, {Project: testProject, Service: "../x"}, {Project: testProject, Service: "PHP"}} {
		if err := s.Append(bad, lines); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}
	if err := s.Append(Key{Project: testProject, Service: "worker:" + testProject}, lines[:1]); err != nil {
		t.Fatalf("worker key: %v", err)
	}
}

func TestStorePrune(t *testing.T) {
	s, _ := OpenStore(t.TempDir())
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	k := Key{Project: testProject, Service: "web"}
	other := Key{Project: "11111111-1111-4111-8111-111111111111", Service: "php"}
	for d := 0; d < 10; d++ {
		ts := now.AddDate(0, 0, -d)
		_ = s.Append(k, []docker.LogLine{{Time: ts, Text: strings.Repeat("x", 1000)}, {Time: ts.Add(time.Second), Text: "y"}})
	}
	_ = s.Append(other, []docker.LogLine{{Time: now, Text: "gone"}})

	res, err := s.Prune(now, 7, 0, func(p string) bool { return p == testProject })
	if err != nil {
		t.Fatal(err)
	}
	// Days 7, 8, 9 are too old, the other project is unknown; days 2 … 6 get compressed.
	if res.Removed != 4 || res.Compressed != 5 {
		t.Fatalf("prune: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), other.Project)); !os.IsNotExist(err) {
		t.Fatal("the unknown project's directory must be gone")
	}
	got := scanAll(t, s, k, time.Time{}, time.Time{})
	if len(got) != 14 || !got[0].Time.Equal(now.AddDate(0, 0, -6)) {
		t.Fatalf("after prune: %d lines, first %v", len(got), got[0].Time)
	}
	// A late write to a compressed day lands next to it and is read with it.
	late := now.AddDate(0, 0, -3).Add(time.Hour)
	_ = s.Append(k, []docker.LogLine{{Time: late, Text: "late"}})
	if got := scanAll(t, s, k, late, late); texts(got) != "late" {
		t.Fatalf("late line: %s", texts(got))
	}
	if _, err := s.Prune(now, 7, 0, nil); err != nil {
		t.Fatal(err)
	}
	if got := scanAll(t, s, k, now.AddDate(0, 0, -3), now.AddDate(0, 0, -3).Add(2*time.Hour)); texts(got) != strings.Repeat("x", 1000)+",y,late" {
		t.Fatalf("merged day: %d lines", len(got))
	}
	if _, total, _ := s.Tail(context.Background(), k, time.Time{}, 1); total != 15 {
		t.Fatalf("count over compressed days: %d", total)
	}

	// The size limit drops the oldest days first.
	u, _ := s.Usage()
	if _, err := s.Prune(now, 30, u.Bytes/2, nil); err != nil {
		t.Fatal(err)
	}
	u2, _ := s.Usage()
	if u2.Bytes > u.Bytes/2 || !u2.Oldest.After(u.Oldest) {
		t.Fatalf("size limit: %+v → %+v", u, u2)
	}
	if got := scanAll(t, s, k, now.Truncate(24*time.Hour), time.Time{}); len(got) != 2 {
		t.Fatal("today must survive the size limit")
	}

	if err := s.Clear(now); err != nil || s.Has(k) || !s.ClearedAt().Equal(now) {
		t.Fatalf("clear: %v", err)
	}
	_ = s.Append(k, []docker.LogLine{{Time: now, Text: "z"}})
	if err := s.RemoveProject(testProject); err != nil || s.Has(k) {
		t.Fatalf("remove project: %v", err)
	}
}

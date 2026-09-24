package cron

import (
	"errors"
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		expr, from, want string
	}{
		{"* * * * *", "2026-09-24 10:15", "2026-09-24 10:16"},
		{"*/15 * * * *", "2026-09-24 10:15", "2026-09-24 10:30"},
		{"*/15 * * * *", "2026-09-24 10:59", "2026-09-24 11:00"},
		{"5 * * * *", "2026-09-24 10:05", "2026-09-24 11:05"},
		{"0 3 * * *", "2026-09-24 10:00", "2026-09-25 03:00"},
		{"30 2 * * 1-5", "2026-09-25 03:00", "2026-09-28 02:30"}, // Friday → Monday
		{"0 0 1 * *", "2026-09-24 10:00", "2026-10-01 00:00"},
		{"0 0 1 1 *", "2026-09-24 10:00", "2027-01-01 00:00"},
		{"0 12 * JAN,jul MON", "2026-09-24 10:00", "2027-01-04 12:00"},
		{"0 0 * * 7", "2026-09-24 10:00", "2026-09-27 00:00"}, // 7 is Sunday
		{"0 0 * * sun", "2026-09-24 10:00", "2026-09-27 00:00"},
		{"10/20 * * * *", "2026-09-24 10:31", "2026-09-24 10:50"},
		{"1-30/10 * * * *", "2026-09-24 10:22", "2026-09-24 11:01"},
		{"0 0 29 2 *", "2026-09-24 10:00", "2028-02-29 00:00"},
		// Both day fields restricted: either matches (the 1st, or any Friday).
		{"0 0 1 * 5", "2026-09-24 10:00", "2026-09-25 00:00"},
		{"0 0 1 * 5", "2026-09-26 10:00", "2026-10-01 00:00"},
		// A step on the weekday counts as "*": both must match.
		{"0 0 13 * */1", "2026-09-24 10:00", "2026-10-13 00:00"},
		{"@hourly", "2026-09-24 10:00", "2026-09-24 11:00"},
		{"@daily", "2026-09-24 10:00", "2026-09-25 00:00"},
		{"@weekly", "2026-09-24 10:00", "2026-09-27 00:00"},
		{"@monthly", "2026-09-24 10:00", "2026-10-01 00:00"},
		{"@yearly", "2026-09-24 10:00", "2027-01-01 00:00"},
	} {
		s := mustParse(t, tc.expr)
		got, ok := s.Next(at(tc.from))
		if !ok || !got.Equal(at(tc.want)) {
			t.Errorf("%q after %s: got %s (%v), want %s", tc.expr, tc.from, got.Format("2006-01-02 15:04 Mon"), ok, tc.want)
		}
		if !s.Matches(at(tc.want)) {
			t.Errorf("%q must match %s", tc.expr, tc.want)
		}
	}
}

func TestNextIgnoresSeconds(t *testing.T) {
	s := mustParse(t, "* * * * *")
	got, _ := s.Next(time.Date(2026, 9, 24, 10, 15, 59, 999, time.UTC))
	if !got.Equal(at("2026-09-24 10:16")) {
		t.Fatalf("got %s", got)
	}
}

func TestNextAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	// 29 March 2026: 02:00 jumps to 03:00. A 02:30 job skips that day.
	s := mustParse(t, "30 2 * * *")
	got, _ := s.Next(time.Date(2026, 3, 28, 12, 0, 0, 0, berlin))
	if want := time.Date(2026, 3, 30, 2, 30, 0, 0, berlin); !got.Equal(want) {
		t.Errorf("spring forward: got %s, want %s", got, want)
	}
	// 25 October 2026: 03:00 falls back to 02:00. The repeated 02:30 fires once.
	first, _ := s.Next(time.Date(2026, 10, 25, 1, 0, 0, 0, berlin))
	second, _ := s.Next(first)
	if first.Day() != 25 || second.Day() != 26 {
		t.Errorf("fall back: %s then %s", first, second)
	}
	// An hourly job keeps firing through the repeated hour without looping.
	h := mustParse(t, "0 * * * *")
	runs := h.NextN(time.Date(2026, 10, 25, 0, 30, 0, 0, berlin), 4)
	for i := 1; i < len(runs); i++ {
		if !runs[i].After(runs[i-1]) {
			t.Fatalf("not increasing: %v", runs)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 13 *", "* * * * 8", "*/0 * * * *", "*/61 * * * *", "5-1 * * * *", "a * * * *",
		"1,,2 * * * *", "@reboot", "@every 5m", "0 0 30 2 *", "0 0 31 4,6,9,11 *", "-1 * * * *",
	} {
		if _, err := Parse(expr); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", expr, err)
		}
	}
}

func TestNextN(t *testing.T) {
	s := mustParse(t, "0 */6 * * *")
	got := s.NextN(at("2026-09-24 10:00"), 3)
	want := []string{"2026-09-24 12:00", "2026-09-24 18:00", "2026-09-25 00:00"}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !got[i].Equal(at(want[i])) {
			t.Errorf("run %d: %s, want %s", i, got[i], want[i])
		}
	}
	if (Schedule{}).NextN(at("2026-09-24 10:00"), 3) == nil || len((Schedule{}).NextN(at("2026-09-24 10:00"), 3)) != 0 {
		t.Error("the zero schedule never fires")
	}
}

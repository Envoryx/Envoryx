package cron

import (
	"testing"
	"time"
)

// A schedule Parse accepts fires (it refuses the others), at a minute it matches.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"*/5 * * * *", "0 3 * * 1-5", "@daily", "0 0 29 2 *", "0 0 31 4 *", "59 23 31 12 0", "1,2,3-10/2 * * jan-mar mon", "* * * * * *", "0 0 30 2 *"} {
		f.Add(s)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		f.Skip("no time zone data")
	}
	from := time.Date(2026, 3, 29, 1, 30, 0, 0, berlin)
	f.Fuzz(func(t *testing.T, expr string) {
		s, err := Parse(expr)
		if err != nil {
			return
		}
		next, ok := s.Next(from)
		if !ok {
			t.Fatalf("%q was accepted but never fires", expr)
		}
		if !next.After(from) || !s.Matches(next) {
			t.Fatalf("%q: next %v after %v", expr, next, from)
		}
		if _, err := Parse(s.String()); err != nil {
			t.Fatalf("%q: its own text %q does not parse: %v", expr, s.String(), err)
		}
	})
}

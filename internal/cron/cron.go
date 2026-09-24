// Package cron parses the five-field schedules of crontab(5) and computes when they fire.
//
// Supported: numbers, "*", ranges (1-5), steps (*/15, 1-30/5, 10/20), lists (1,15,30),
// month and weekday names (JAN, MON …), 7 as Sunday and the macros @yearly, @annually,
// @monthly, @weekly, @daily, @midnight and @hourly. Like Vixie cron, a schedule that
// restricts both the day of the month and the weekday fires when either matches.
// Seconds, @reboot and @every are not part of the format and are refused.
package cron

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is wrapped by every parse error.
var ErrInvalid = errors.New("invalid cron schedule")

// searchYears bounds Next: a schedule that does not fire within this many years (30
// February) never fires, and Parse refuses it.
const searchYears = 5

// Schedule is a parsed cron expression. The zero value never fires.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domAny/dowAny record a field written as "*" (or a "*/n" step): only when both day
	// fields are restricted does either one suffice.
	domAny, dowAny bool
	expr           string
}

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	minuteField = field{name: "minute", min: 0, max: 59}
	hourField   = field{name: "hour", min: 0, max: 23}
	domField    = field{name: "day of month", min: 1, max: 31}
	monthField  = field{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	// 7 is accepted as Sunday and folded onto 0 after parsing.
	dowField = field{name: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Parse reads a cron expression. It refuses schedules that never fire.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	spec := expr
	if strings.HasPrefix(spec, "@") {
		m, ok := macros[strings.ToLower(spec)]
		if !ok {
			return Schedule{}, fmt.Errorf("%w: unknown macro %q (use @hourly, @daily, @weekly, @monthly or @yearly)", ErrInvalid, spec)
		}
		spec = m
	}
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("%w: expected 5 fields (minute hour day-of-month month day-of-week), got %d", ErrInvalid, len(parts))
	}
	s := Schedule{expr: expr}
	var err error
	if s.minute, _, err = parseField(parts[0], minuteField); err != nil {
		return Schedule{}, err
	}
	if s.hour, _, err = parseField(parts[1], hourField); err != nil {
		return Schedule{}, err
	}
	if s.dom, s.domAny, err = parseField(parts[2], domField); err != nil {
		return Schedule{}, err
	}
	if s.month, _, err = parseField(parts[3], monthField); err != nil {
		return Schedule{}, err
	}
	if s.dow, s.dowAny, err = parseField(parts[4], dowField); err != nil {
		return Schedule{}, err
	}
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	// A fixed reference keeps the check independent of today's date: five years from any
	// start cover a leap day.
	if _, ok := s.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); !ok {
		return Schedule{}, fmt.Errorf("%w: %q never fires", ErrInvalid, expr)
	}
	return s, nil
}

// parseField returns the bit set of one field and whether it was written as "*" (with or
// without a step).
func parseField(text string, f field) (uint64, bool, error) {
	var set uint64
	star := false
	for _, part := range strings.Split(text, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("%w: empty entry in the %s field %q", ErrInvalid, f.name, text)
		}
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > f.max {
				return 0, false, fmt.Errorf("%w: step %q in the %s field must be 1-%d", ErrInvalid, stepText, f.name, f.max)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
			if len(strings.Split(text, ",")) == 1 {
				star = true
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = value(a, f); err != nil {
				return 0, false, err
			}
			if hi, err = value(b, f); err != nil {
				return 0, false, err
			}
			if lo > hi {
				return 0, false, fmt.Errorf("%w: range %q in the %s field runs backwards", ErrInvalid, rng, f.name)
			}
		default:
			v, err := value(rng, f)
			if err != nil {
				return 0, false, err
			}
			lo = v
			// "10/20" means from 10 to the end in steps of 20; a plain "10" is just 10.
			if !hasStep {
				hi = v
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, star, nil
}

func value(text string, f field) (int, error) {
	if v, ok := f.names[strings.ToLower(text)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(text)
	if err != nil || v < f.min || v > f.max {
		return 0, fmt.Errorf("%w: %q is not a valid %s (%d-%d)", ErrInvalid, text, f.name, f.min, f.max)
	}
	return v, nil
}

// String returns the expression as written.
func (s Schedule) String() string { return s.expr }

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

func (s Schedule) dayMatches(t time.Time) bool {
	dom, dow := has(s.dom, t.Day()), has(s.dow, int(t.Weekday()))
	if s.domAny || s.dowAny {
		return dom && dow
	}
	return dom || dow
}

// Matches reports whether the schedule fires in the minute of t (in t's location).
func (s Schedule) Matches(t time.Time) bool {
	return has(s.minute, t.Minute()) && has(s.hour, t.Hour()) && has(s.month, int(t.Month())) && s.dayMatches(t)
}

// Next returns the first minute strictly after t at which the schedule fires, in t's
// location. ok is false when it does not fire within the search horizon. Wall-clock
// times skipped by a DST change are skipped, repeated ones fire once.
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	if s.minute == 0 || s.hour == 0 || s.month == 0 || (s.dom == 0 && s.dow == 0) {
		return time.Time{}, false
	}
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(searchYears, 0, 0)
	for t.Before(limit) {
		if !has(s.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !has(s.hour, t.Hour()) {
			next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			if !next.After(t) { // a repeated hour at the end of DST
				next = t.Add(time.Hour).Truncate(time.Hour)
			}
			t = next
			continue
		}
		if !has(s.minute, t.Minute()) {
			// Jump to the next minute of the set within this hour, if any.
			rest := s.minute >> uint(t.Minute()+1) << uint(t.Minute()+1)
			if rest == 0 {
				t = t.Add(time.Duration(60-t.Minute()) * time.Minute)
				continue
			}
			t = t.Add(time.Duration(bits.TrailingZeros64(rest)-t.Minute()) * time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// NextN returns up to n upcoming fire times after t.
func (s Schedule) NextN(t time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for len(out) < n {
		next, ok := s.Next(t)
		if !ok {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

package project

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// minitestSummary is the line minitest ends a run with, "7 runs, 11 assertions, 0
	// failures, 0 errors, 0 skips"; a run in parallel workers prints one for all of them.
	minitestSummary = regexp.MustCompile(`(?m)^(\d+) runs, \d+ assertions, (\d+) failures, (\d+) errors, (\d+) skips$`)
	minitestSeconds = regexp.MustCompile(`(?m)^Finished in ([0-9.]+)s`)
	// minitestTest is the line under "Failure:" or "Error:": Class#test_name, with the
	// location of the failed assertion for a failure ("[test/models/post_test.rb:9]").
	minitestTest = regexp.MustCompile(`^(\S+)#(\S+?)(?: \[(.+):(\d+)\])?:$`)
	// minitestNumber prefixes the failures plain minitest lists at the end ("  1) Failure:").
	minitestNumber = regexp.MustCompile(`^\d+\) `)
	// minitestRerun is the command Rails prints after a failure to run the test again,
	// pointing at the line the test starts on.
	minitestRerun = regexp.MustCompile(`^bin/rails test (\S+):(\d+)$`)
)

// parseMinitest reads the result of a minitest run (rails test) from what it printed,
// since minitest writes no JUnit report without a reporter gem in the bundle: the counts
// from its summary line, the failed tests from the "Failure:" and "Error:" blocks Rails
// prints as they happen (and plain minitest's numbered ones at the end). ok is false
// when the output has no summary line, as when the tests did not load.
func parseMinitest(text, root string) (res TestResult, ok bool) {
	sums := minitestSummary.FindAllStringSubmatch(text, -1)
	if len(sums) == 0 {
		return TestResult{}, false
	}
	sum := sums[len(sums)-1]
	num := func(s string) int { n, _ := strconv.Atoi(s); return n }
	res = TestResult{Report: true, Tests: num(sum[1]), Failures: num(sum[2]), Errors: num(sum[3]), Skipped: num(sum[4]), Failed: []TestCase{}}
	if m := minitestSeconds.FindAllStringSubmatch(text, -1); len(m) > 0 {
		res.Seconds, _ = strconv.ParseFloat(m[len(m)-1][1], 64)
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		label := minitestNumber.ReplaceAllString(strings.TrimSpace(lines[i]), "")
		kind := map[string]string{"Failure:": "failure", "Error:": "error"}[label]
		if kind == "" || i+1 >= len(lines) {
			continue
		}
		m := minitestTest.FindStringSubmatch(strings.TrimSpace(lines[i+1]))
		if m == nil {
			continue
		}
		tc := TestCase{Name: m[2], Class: m[1], Kind: kind, File: relativeTo(m[3], root), Line: num(m[4])}
		// The message runs to the next blank line; an error's backtrace follows its
		// first line.
		var body []string
		for i += 2; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			body = append(body, strings.TrimRight(lines[i], " \t"))
		}
		// Rails 8.1 leaves two blank lines before the rerun command, earlier versions one.
		next := i + 1
		for next < len(lines) && next <= i+2 && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next < len(lines) {
			if r := minitestRerun.FindStringSubmatch(strings.TrimSpace(lines[next])); r != nil {
				tc.File, tc.Line = relativeTo(r[1], root), num(r[2])
				i = next
			}
		}
		if len(body) > 0 {
			tc.Message = strings.TrimSpace(body[0])
			if len(body) > 1 {
				tc.Details = strings.Join(body, "\n")
				if len(tc.Details) > maxDetails {
					tc.Details = tc.Details[:maxDetails] + "\n…"
				}
			}
		}
		if len(res.Failed) < maxFailedCases {
			res.Failed = append(res.Failed, tc)
		}
	}
	// The output kept is the end of the run: what scrolled out of it is counted as more.
	if bad := res.Failures + res.Errors; bad > len(res.Failed) {
		res.More = bad - len(res.Failed)
	}
	return res, true
}

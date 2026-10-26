package project

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Readers for runners that write no JUnit report here: RSpec without
// rspec_junit_formatter, Django's manage.py test, and Vitest or Jest behind a package.json
// script. Like parseMinitest they take the counts from the summary and the failed tests
// from the blocks the runner prints; ok is false when the summary is missing.

var (
	// rspecSummary is "4 examples, 2 failures, 1 pending" (and "1 example, 0 failures").
	rspecSummary = regexp.MustCompile(`(?m)^(\d+) examples?, (\d+) failures?(?:, (\d+) pending)?`)
	rspecSeconds = regexp.MustCompile(`(?m)^Finished in ([0-9.]+) seconds?`)
	// rspecFailure opens a failure block: "  1) Post fails on purpose".
	rspecFailure = regexp.MustCompile(`^  (\d+)\) (.+)$`)
	// rspecRerun is a line of the "Failed examples:" list: the example's own line.
	rspecRerun = regexp.MustCompile(`(?m)^rspec (\S+?):(\d+) # (.+)$`)
	// rspecBacktrace is a location line of a failure block.
	rspecBacktrace = regexp.MustCompile(`^# (\S+?):(\d+)`)
	// rspecException is the exception class RSpec prints above an error's message.
	rspecException = regexp.MustCompile(`^[A-Z][\w:]*(Error|Exception)\w*:$`)
)

// parseRSpec reads RSpec's progress or documentation output.
func parseRSpec(text, root string) (res TestResult, ok bool) {
	sums := rspecSummary.FindAllStringSubmatch(text, -1)
	if len(sums) == 0 {
		return TestResult{}, false
	}
	sum := sums[len(sums)-1]
	res = TestResult{Report: true, Tests: atoi(sum[1]), Skipped: atoi(sum[3]), Failed: []TestCase{}}
	if m := rspecSeconds.FindAllStringSubmatch(text, -1); len(m) > 0 {
		res.Seconds = parseSeconds(m[len(m)-1][1])
	}
	failures := atoi(sum[2])
	lines := strings.Split(text, "\n")
	inFailures := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		switch {
		case line == "Failures:":
			inFailures = true
			continue
		case !inFailures:
			continue
		case strings.HasPrefix(line, "Finished in "), line == "Failed examples:":
			inFailures = false
			continue
		}
		m := rspecFailure.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		tc := TestCase{Name: m[2], Kind: "failure"}
		var body []string
		for i++; i < len(lines); i++ {
			l := strings.TrimRight(lines[i], " \t")
			if rspecFailure.MatchString(l) || strings.HasPrefix(l, "Finished in ") {
				i--
				break
			}
			body = append(body, strings.TrimPrefix(l, "     "))
		}
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		var msg []string
		for _, l := range body {
			t := strings.TrimSpace(l)
			if b := rspecBacktrace.FindStringSubmatch(t); b != nil {
				if tc.File == "" {
					tc.File, tc.Line = rspecPath(b[1], root), atoi(b[2])
				}
				continue
			}
			if t == "" || strings.HasPrefix(t, "Failure/Error:") {
				continue
			}
			if rspecException.MatchString(t) {
				tc.Kind = "error"
			}
			msg = append(msg, t)
		}
		if len(msg) > 0 {
			tc.Message = msg[0]
			if tc.Kind == "error" && len(msg) > 1 {
				tc.Message = msg[0] + " " + msg[1]
			}
		}
		tc.Details = cutDetails(strings.Join(body, "\n"))
		if tc.Kind == "error" {
			res.Errors++
		}
		if len(res.Failed) < maxFailedCases {
			res.Failed = append(res.Failed, tc)
		}
	}
	res.Failures = failures - res.Errors
	if res.Failures < 0 {
		res.Failures = 0
	}
	locateRSpec(&res, text, root)
	if bad := res.Failures + res.Errors; bad > len(res.Failed) {
		res.More = bad - len(res.Failed)
	}
	return res, true
}

// locateRSpec points the failed examples at the line they start on, from the "Failed
// examples:" list RSpec ends with; the JUnit formatter names the file only.
func locateRSpec(res *TestResult, text, root string) {
	at := map[string][2]string{}
	for _, m := range rspecRerun.FindAllStringSubmatch(text, -1) {
		at[strings.TrimSpace(m[3])] = [2]string{m[1], m[2]}
	}
	for i := range res.Failed {
		tc := &res.Failed[i]
		if loc, ok := at[tc.Name]; ok {
			tc.File, tc.Line = rspecPath(loc[0], root), atoi(loc[1])
		} else if tc.File != "" {
			tc.File = rspecPath(tc.File, root)
		}
	}
}

// rspecPath makes RSpec's "./spec/x_spec.rb" relative to the project like the others.
func rspecPath(p, root string) string {
	return strings.TrimPrefix(relativeTo(p, root), "./")
}

var (
	// djangoRan and djangoOutcome are unittest's summary: "Ran 4 tests in 0.002s", then
	// "FAILED (failures=1, errors=1, skipped=1)" or "OK (skipped=1)".
	djangoRan     = regexp.MustCompile(`(?m)^Ran (\d+) tests? in ([0-9.]+)s$`)
	djangoOutcome = regexp.MustCompile(`(?m)^(OK|FAILED)(?: \((.+)\))?$`)
	djangoCount   = regexp.MustCompile(`(\w+)=(\d+)`)
	// djangoHeader opens a failure: "FAIL: test_fails (blog.tests.PostTests.test_fails)";
	// before Python 3.11 the parentheses hold the class only.
	djangoHeader = regexp.MustCompile(`^(FAIL|ERROR): (\S+) \((\S+?)\)$`)
	djangoFrame  = regexp.MustCompile(`^\s*File "(.+)", line (\d+), in (\S+)`)
)

// parseDjango reads the output of manage.py test (unittest's text runner).
func parseDjango(text, root string) (res TestResult, ok bool) {
	ran := djangoRan.FindAllStringSubmatch(text, -1)
	if len(ran) == 0 {
		return TestResult{}, false
	}
	last := ran[len(ran)-1]
	res = TestResult{Report: true, Tests: atoi(last[1]), Failed: []TestCase{}}
	res.Seconds = parseSeconds(last[2])
	if out := djangoOutcome.FindAllStringSubmatch(text, -1); len(out) > 0 {
		for _, c := range djangoCount.FindAllStringSubmatch(out[len(out)-1][2], -1) {
			switch c[1] {
			case "failures":
				res.Failures = atoi(c[2])
			case "errors":
				res.Errors = atoi(c[2])
			case "skipped":
				res.Skipped = atoi(c[2])
			}
		}
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := djangoHeader.FindStringSubmatch(strings.TrimRight(lines[i], " \t"))
		if m == nil {
			continue
		}
		class := strings.TrimSuffix(m[3], "."+m[2])
		tc := TestCase{Name: m[2], Class: class, Kind: map[string]string{"FAIL": "failure", "ERROR": "error"}[m[1]]}
		var body []string
		i++
		if i < len(lines) && strings.HasPrefix(lines[i], "-----") {
			i++
		}
		for ; i < len(lines); i++ {
			l := strings.TrimRight(lines[i], " \t")
			if strings.HasPrefix(l, "=====") || strings.HasPrefix(l, "-----") {
				i--
				break
			}
			body = append(body, l)
		}
		for len(body) > 0 && body[len(body)-1] == "" {
			body = body[:len(body)-1]
		}
		for _, l := range body {
			if f := djangoFrame.FindStringSubmatch(l); f != nil && strings.HasPrefix(f[1], root+"/") {
				tc.File, tc.Line = relativeTo(f[1], root), atoi(f[2]) // the innermost frame in the project
			}
		}
		if len(body) > 0 {
			tc.Message = strings.TrimSpace(body[len(body)-1])
		}
		tc.Details = cutDetails(strings.ReplaceAll(strings.Join(body, "\n"), root+"/", ""))
		if len(res.Failed) < maxFailedCases {
			res.Failed = append(res.Failed, tc)
		}
	}
	if bad := res.Failures + res.Errors; bad > len(res.Failed) {
		res.More = bad - len(res.Failed)
	}
	return res, true
}

var (
	// vitestTests is Vitest's summary line: "Tests  2 failed | 1 passed | 1 skipped (4)".
	vitestTests    = regexp.MustCompile(`(?m)^\s+Tests\s+(.+) \((\d+)\)$`)
	vitestDuration = regexp.MustCompile(`(?m)^\s+Duration\s+([0-9.]+)(ms|s)\b`)
	vitestFail     = regexp.MustCompile(`^ FAIL\s+(\S+) > (.+)$`)
	vitestAt       = regexp.MustCompile(`^ ❯ (\S+?):(\d+):\d+$`)
	// jestTests is Jest's: "Tests:       2 failed, 1 skipped, 1 passed, 4 total".
	jestTests = regexp.MustCompile(`(?m)^Tests:\s+(.+), (\d+) total$`)
	jestTime  = regexp.MustCompile(`(?m)^Time:\s+([0-9.]+) s`)
	jestFail  = regexp.MustCompile(`^  ● (.+)$`)
	jestAt    = regexp.MustCompile(`^\s+at .*\(([^()]+?):(\d+):\d+\)$`)
	jsCount   = regexp.MustCompile(`(\d+) (failed|passed|skipped|todo|pending)`)
)

// parseJSTests reads what Vitest or Jest printed behind a package.json test script.
func parseJSTests(text, root string) (TestResult, bool) {
	if res, ok := parseVitest(text, root); ok {
		return res, true
	}
	return parseJest(text, root)
}

func jsCounts(res *TestResult, s string) {
	for _, c := range jsCount.FindAllStringSubmatch(s, -1) {
		switch c[2] {
		case "failed":
			res.Failures = atoi(c[1])
		case "skipped", "todo", "pending":
			res.Skipped += atoi(c[1])
		}
	}
}

func parseVitest(text, root string) (res TestResult, ok bool) {
	sums := vitestTests.FindAllStringSubmatch(text, -1)
	if len(sums) == 0 {
		return TestResult{}, false
	}
	sum := sums[len(sums)-1]
	res = TestResult{Report: true, Tests: atoi(sum[2]), Failed: []TestCase{}}
	jsCounts(&res, sum[1])
	if d := vitestDuration.FindAllStringSubmatch(text, -1); len(d) > 0 {
		v := parseSeconds(d[len(d)-1][1])
		if d[len(d)-1][2] == "ms" {
			v /= 1000
		}
		res.Seconds = v
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := vitestFail.FindStringSubmatch(strings.TrimRight(lines[i], " \t"))
		if m == nil {
			continue
		}
		path := strings.Split(m[2], " > ")
		tc := TestCase{Name: path[len(path)-1], Class: strings.Join(path[:len(path)-1], " > "), File: relativeTo(m[1], root), Kind: "failure"}
		var body []string
		for i++; i < len(lines); i++ {
			l := strings.TrimRight(lines[i], " \t")
			if strings.HasPrefix(l, "⎯") || vitestFail.MatchString(l) {
				i--
				break
			}
			if a := vitestAt.FindStringSubmatch(l); a != nil && tc.Line == 0 {
				tc.Line = atoi(a[2])
			}
			body = append(body, l)
		}
		for len(body) > 0 && body[len(body)-1] == "" {
			body = body[:len(body)-1]
		}
		if len(body) > 0 {
			tc.Message = strings.TrimSpace(body[0])
			if !strings.HasPrefix(tc.Message, "AssertionError") {
				tc.Kind = "error"
			}
		}
		tc.Details = cutDetails(strings.Join(body, "\n"))
		res.Failed = appendCase(res.Failed, tc)
	}
	splitErrors(&res)
	return res, true
}

func parseJest(text, root string) (res TestResult, ok bool) {
	sums := jestTests.FindAllStringSubmatch(text, -1)
	if len(sums) == 0 {
		return TestResult{}, false
	}
	sum := sums[len(sums)-1]
	res = TestResult{Report: true, Tests: atoi(sum[2]), Failed: []TestCase{}}
	jsCounts(&res, sum[1])
	if t := jestTime.FindAllStringSubmatch(text, -1); len(t) > 0 {
		res.Seconds = parseSeconds(t[len(t)-1][1])
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := jestFail.FindStringSubmatch(strings.TrimRight(lines[i], " \t"))
		if m == nil {
			continue
		}
		path := strings.Split(m[1], " › ")
		tc := TestCase{Name: path[len(path)-1], Class: strings.Join(path[:len(path)-1], " › "), Kind: "failure"}
		var body []string
		for i++; i < len(lines); i++ {
			l := strings.TrimRight(lines[i], " \t")
			if jestFail.MatchString(l) || strings.HasPrefix(l, "Test Suites:") {
				i--
				break
			}
			if a := jestAt.FindStringSubmatch(l); a != nil && tc.File == "" && !strings.Contains(a[1], "node_modules") {
				tc.File, tc.Line = relativeTo(a[1], root), atoi(a[2])
			}
			body = append(body, strings.TrimPrefix(l, "    "))
		}
		for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
			body = body[1:]
		}
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		if len(body) > 0 {
			tc.Message = strings.TrimSpace(body[0])
			if !strings.HasPrefix(tc.Message, "expect(") {
				tc.Kind = "error"
			}
		}
		tc.Details = cutDetails(strings.Join(body, "\n"))
		res.Failed = appendCase(res.Failed, tc)
	}
	splitErrors(&res)
	return res, true
}

// splitErrors moves the failed tests that threw rather than failed an assertion from the
// failures to the errors, so the counts match the cases shown.
func splitErrors(res *TestResult) {
	for _, tc := range res.Failed {
		if tc.Kind == "error" && res.Failures > 0 {
			res.Failures--
			res.Errors++
		}
	}
	if bad := res.Failures + res.Errors; bad > len(res.Failed) {
		res.More = bad - len(res.Failed)
	}
}

func appendCase(cases []TestCase, tc TestCase) []TestCase {
	if len(cases) < maxFailedCases {
		return append(cases, tc)
	}
	return cases
}

func cutDetails(s string) string {
	if len(s) > maxDetails {
		return s[:maxDetails] + "\n…"
	}
	return s
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

var (
	// goBuildFailed is the details gotestsum gives the made-up "TestMain" failure of a
	// package that did not compile: "FAIL	app/calc [build failed]".
	goBuildFailed = regexp.MustCompile(`FAIL\s+(\S+) \[build failed\]`)
	// goCompileError is a compiler error line under gotestsum's "=== Errors", the path
	// relative to the module: "calc/calc.go:4:28: cannot use …".
	goCompileError = regexp.MustCompile(`^(\S+\.go):(\d+):(?:\d+:)? (.+)$`)
)

// goBuildFailures turns the "TestMain: Failed" gotestsum reports for a package that did
// not compile into the compiler's errors, which it prints only in its output: the
// package as the name, the first error as the message and its file and line.
func goBuildFailures(res *TestResult, output string) {
	var errs []string
	if _, rest, ok := strings.Cut(output, "\n=== Errors\n"); ok {
		rest, _, _ = strings.Cut(rest, "\nDONE ")
		for _, l := range strings.Split(rest, "\n") {
			if goCompileError.MatchString(strings.TrimSpace(l)) {
				errs = append(errs, strings.TrimSpace(l))
			}
		}
	}
	builds := 0
	for _, f := range res.Failed {
		if f.Name == "TestMain" && goBuildFailed.MatchString(f.Details+f.Message) {
			builds++
		}
	}
	for i, f := range res.Failed {
		m := goBuildFailed.FindStringSubmatch(f.Details + f.Message)
		if f.Name != "TestMain" || m == nil {
			continue
		}
		pkg := m[1]
		// The errors of this package: their directory ends its import path. With one
		// broken package all of them are its own.
		var own []string
		for _, e := range errs {
			dir := filepath.Dir(goCompileError.FindStringSubmatch(e)[1])
			if builds == 1 || pkg == dir || strings.HasSuffix(pkg, "/"+dir) || (dir == "." && !strings.Contains(pkg, "/")) {
				own = append(own, e)
			}
		}
		tc := TestCase{Name: pkg, Kind: "error", Message: "build failed", Details: f.Details}
		if len(own) > 0 {
			first := goCompileError.FindStringSubmatch(own[0])
			tc.File, tc.Line, tc.Message = first[1], atoi(first[2]), first[3]
			tc.Details = cutDetails(strings.Join(own, "\n"))
		}
		res.Failed[i] = tc
	}
}

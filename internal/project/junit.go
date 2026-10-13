package project

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// TestCase is a test that did not pass, as the report describes it.
type TestCase struct {
	Name  string `json:"name"`
	Class string `json:"class,omitempty"`
	// File and Line point at the test, relative to the project directory where the
	// report named a path inside the container.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Kind is failure or error.
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Details is the failure's full text (assertion diff, stack trace), cut to 4 KiB.
	Details string `json:"details,omitempty"`
}

// TestResult is what a run's JUnit report says, or what the runner printed for rails
// test, RSpec without the JUnit formatter, Django and Vitest or Jest behind an npm script
// (testoutput.go). Report is false when neither says anything (another npm script): then
// only the exit code counts.
type TestResult struct {
	Report   bool       `json:"report"`
	Tests    int        `json:"tests"`
	Failures int        `json:"failures"`
	Errors   int        `json:"errors"`
	Skipped  int        `json:"skipped"`
	Seconds  float64    `json:"seconds"`
	Failed   []TestCase `json:"failed"`
	// More is how many failed cases were left out of Failed.
	More int `json:"more,omitempty"`
	// Output is the end of what the run printed, for runs without a report and for
	// reading a failure later.
	Output string `json:"output,omitempty"`
}

const (
	maxFailedCases = 100
	maxDetails     = 4 << 10
)

// junitNode is any element of a JUnit report; testcase elements are found wherever they
// are nested (PHPUnit nests suites per class and data set, Playwright per file).
type junitNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr  `xml:",any,attr"`
	Children []junitNode `xml:",any"`
	Text     string      `xml:",chardata"`
}

func (n junitNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// parseJUnit reads a JUnit XML report. root is the project's mount point inside the
// container, cut from the paths the report names.
func parseJUnit(r io.Reader, root string) (TestResult, error) {
	var doc junitNode
	if err := xml.NewDecoder(io.LimitReader(r, 32<<20)).Decode(&doc); err != nil {
		return TestResult{}, fmt.Errorf("read the JUnit report: %w", err)
	}
	res := TestResult{Report: true, Failed: []TestCase{}}
	var walk func(n junitNode)
	walk = func(n junitNode) {
		if n.XMLName.Local != "testcase" {
			for _, c := range n.Children {
				walk(c)
			}
			return
		}
		res.Tests++
		res.Seconds += parseSeconds(n.attr("time"))
		for _, c := range n.Children {
			switch c.XMLName.Local {
			case "skipped":
				res.Skipped++
			case "failure", "error":
				if c.XMLName.Local == "failure" {
					res.Failures++
				} else {
					res.Errors++
				}
				if len(res.Failed) == maxFailedCases {
					res.More++
					continue
				}
				res.Failed = append(res.Failed, failedCase(n, c, root))
			}
		}
	}
	walk(doc)
	res.Seconds = roundSeconds(res.Seconds)
	return res, nil
}

// parseSeconds reads a duration in seconds as a report or a runner prints it; anything
// that is not a finite, non-negative number (NaN, -1, more digits than a float holds)
// counts as 0, since the result has to go into JSON.
func parseSeconds(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// roundSeconds rounds a sum of durations to milliseconds; one too large for a float is
// dropped like an unreadable one.
func roundSeconds(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) || v < 0 {
		return 0
	}
	return math.Round(v*1000) / 1000
}

func failedCase(tc, f junitNode, root string) TestCase {
	class := tc.attr("classname")
	if class == "" {
		class = tc.attr("class")
	}
	details := strings.TrimSpace(stripEscapedANSI(f.Text))
	msg := strings.TrimSpace(stripEscapedANSI(f.attr("message")))
	if msg == "" {
		// PHPUnit writes no message attribute and starts the text with the test's id
		// (Class::method); the assertion follows on the next line.
		for _, line := range strings.Split(details, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasSuffix(line, "::"+tc.attr("name")) {
				msg = line
				break
			}
		}
	}
	// Pest glues the test's name in front of the message ("failingFailed asserting…").
	if name := tc.attr("name"); name != "" && strings.HasPrefix(msg, name) && len(msg) > len(name) {
		msg = strings.TrimSpace(msg[len(name):])
	}
	if m := goLocation.FindStringSubmatch(details); m != nil && (msg == "" || msg == "Failed") {
		// gotestsum's message is always "Failed"; what the test reported is in the output,
		// behind the file and line t.Errorf prints.
		msg = strings.TrimSpace(m[3])
	}
	if len(details) > maxDetails {
		details = details[:maxDetails] + "\n…"
	}
	if details == msg {
		details = ""
	}
	// Pest's file attribute is "path::test name".
	file, _, _ := strings.Cut(tc.attr("file"), "::")
	line, _ := strconv.Atoi(tc.attr("line"))
	line = max(line, 0)
	if file == "" || line == 0 {
		if f, l := locationIn(details); f != "" && (file == "" || strings.HasSuffix(f, file) || strings.HasSuffix(file, f)) {
			file, line = f, l
		}
	}
	return TestCase{
		Name: tc.attr("name"), Class: class, File: relativeTo(file, root), Line: line,
		Kind: f.XMLName.Local, Message: relativeTo(msg, root), Details: strings.ReplaceAll(details, root+"/", ""),
	}
}

// locationIn finds the test's file and line in a failure text, for reports that carry
// neither as attributes: pytest ends its traceback with "path.py:LINE: SomeError",
// Playwright's stack names the spec as "at e2e/x.spec.ts:10:53", Pest the test as
// "at tests/Feature/X.php:14", and go test (through gotestsum) indents
// "x_test.go:LINE: message" under "=== RUN". Go prints only the file's name, not its
// package directory.
func locationIn(details string) (string, int) {
	if m := goLocation.FindStringSubmatch(details); m != nil {
		line, _ := strconv.Atoi(m[2])
		return m[1], line
	}
	if m := pytestLocation.FindAllStringSubmatch(details, -1); m != nil {
		last := m[len(m)-1]
		line, _ := strconv.Atoi(last[2])
		return last[1], line
	}
	for _, m := range stackLocation.FindAllStringSubmatch(details, -1) {
		if !strings.Contains(m[1], "node_modules/") {
			line, _ := strconv.Atoi(m[2])
			return m[1], line
		}
	}
	for _, m := range phpLocation.FindAllStringSubmatch(details, -1) {
		if !strings.HasPrefix(m[1], "vendor/") {
			line, _ := strconv.Atoi(m[2])
			return m[1], line
		}
	}
	return "", 0
}

var (
	pytestLocation = regexp.MustCompile(`(?m)^([^\s:]+\.py):(\d+): \w`)
	phpLocation    = regexp.MustCompile(`(?m)^\s*at ([^\s:]+\.php):(\d+)\s*$`)
	goLocation     = regexp.MustCompile(`(?m)^\s+([\w.-]+\.go):(\d+): (.*)$`)
	stackLocation  = regexp.MustCompile(`(?m)^\s*at (?:.*\()?([^\s():]+\.[cm]?[jt]sx?):(\d+):\d+\)?\s*$`)
)

// stripEscapedANSI removes colour codes from report text: pytest --color=yes writes them
// into the JUnit XML as "#x1B[...m", which XML can't carry as a control character.
func stripEscapedANSI(s string) string {
	return escapedANSI.ReplaceAllString(ansiCodes.ReplaceAllString(s, ""), "")
}

var escapedANSI = regexp.MustCompile(`#x1B\[[0-9;?]*[A-Za-z]`)

// relativeTo cuts the project's mount point from a path the report names.
func relativeTo(p, root string) string {
	if rest, ok := strings.CutPrefix(p, root+"/"); ok {
		return rest
	}
	return p
}

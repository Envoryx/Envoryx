package project

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parseTRX reads the Visual Studio test results (TRX) that dotnet test's trx logger
// writes, one TestRun per test project; dotnetTestScript wraps them in one root element.
// The test's class comes from its definition, and "NotExecuted" counts as skipped. root
// is the project's mount point inside the container, cut from the paths the report names.
func parseTRX(r io.Reader, root string) (TestResult, error) {
	var doc junitNode
	if err := xml.NewDecoder(io.LimitReader(r, 32<<20)).Decode(&doc); err != nil {
		return TestResult{}, fmt.Errorf("read the TRX report: %w", err)
	}
	res := TestResult{Report: true, Failed: []TestCase{}}
	classes := map[string]string{}
	var results []junitNode
	var walk func(n junitNode)
	walk = func(n junitNode) {
		switch n.XMLName.Local {
		case "UnitTest":
			for _, c := range n.Children {
				if c.XMLName.Local == "TestMethod" {
					classes[n.attr("id")] = c.attr("className")
				}
			}
			return
		case "UnitTestResult":
			results = append(results, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc)
	for _, n := range results {
		res.Tests++
		if d, err := parseTRXDuration(n.attr("duration")); err == nil {
			res.Seconds += d.Seconds()
		}
		switch n.attr("outcome") {
		case "Passed":
			continue
		case "NotExecuted", "Inconclusive", "Pending", "Disconnected":
			res.Skipped++
			continue
		case "Error", "Aborted", "Timeout":
			res.Errors++
		default: // Failed
			res.Failures++
		}
		if len(res.Failed) == maxFailedCases {
			res.More++
			continue
		}
		res.Failed = append(res.Failed, trxFailedCase(n, classes[n.attr("testId")], root))
	}
	res.Seconds = float64(int(res.Seconds*1000)) / 1000
	return res, nil
}

// trxStackFrame finds a stack frame with a source position: " in /path/File.cs:line 12".
var trxStackFrame = regexp.MustCompile(` in (/\S+):line (\d+)`)

// trxFailedCase turns a failed UnitTestResult into a TestCase: the test name without its
// class prefix, the first line of the error message, message plus stack trace as details,
// and the first source position of the stack trace inside the project as file and line.
func trxFailedCase(n junitNode, class, root string) TestCase {
	var msg, stack string
	for _, out := range n.Children {
		if out.XMLName.Local != "Output" {
			continue
		}
		for _, info := range out.Children {
			if info.XMLName.Local != "ErrorInfo" {
				continue
			}
			for _, c := range info.Children {
				switch c.XMLName.Local {
				case "Message":
					msg = strings.TrimSpace(c.Text)
				case "StackTrace":
					stack = strings.TrimSpace(c.Text)
				}
			}
		}
	}
	name := n.attr("testName")
	if class != "" {
		name = strings.TrimPrefix(name, class+".")
	}
	kind := "failure"
	if o := n.attr("outcome"); o == "Error" || o == "Aborted" || o == "Timeout" {
		kind = "error"
	}
	first, _, _ := strings.Cut(msg, "\n")
	details := strings.TrimSpace(msg + "\n" + stack)
	if len(details) > maxDetails {
		details = details[:maxDetails] + "\n…"
	}
	if details == first {
		details = ""
	}
	tc := TestCase{
		Name: name, Class: class, Kind: kind,
		Message: relativeTo(strings.TrimSpace(first), root), Details: strings.ReplaceAll(details, root+"/", ""),
	}
	for _, m := range trxStackFrame.FindAllStringSubmatch(stack, -1) {
		if rel := relativeTo(m[1], root); rel != m[1] {
			tc.File = rel
			tc.Line, _ = strconv.Atoi(m[2])
			break
		}
	}
	return tc
}

// parseTRXDuration reads a TRX duration, hh:mm:ss.fffffff.
func parseTRXDuration(s string) (time.Duration, error) {
	var h, m int
	var sec float64
	if _, err := fmt.Sscanf(s, "%d:%d:%f", &h, &m, &sec); err != nil {
		return 0, err
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second)), nil
}

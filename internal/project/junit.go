package project

import (
	"encoding/xml"
	"fmt"
	"io"
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

// TestResult is what a run's JUnit report says. Report is false when the suite wrote
// none (npm scripts, Django): then only the exit code counts.
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
		if s, err := strconv.ParseFloat(n.attr("time"), 64); err == nil {
			res.Seconds += s
		}
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
	res.Seconds = float64(int(res.Seconds*1000)) / 1000
	return res, nil
}

func failedCase(tc, f junitNode, root string) TestCase {
	class := tc.attr("classname")
	if class == "" {
		class = tc.attr("class")
	}
	details := strings.TrimSpace(f.Text)
	msg := strings.TrimSpace(f.attr("message"))
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
	if len(details) > maxDetails {
		details = details[:maxDetails] + "\n…"
	}
	if details == msg {
		details = ""
	}
	line, _ := strconv.Atoi(tc.attr("line"))
	return TestCase{
		Name: tc.attr("name"), Class: class, File: relativeTo(tc.attr("file"), root), Line: line,
		Kind: f.XMLName.Local, Message: relativeTo(msg, root), Details: strings.ReplaceAll(details, root+"/", ""),
	}
}

// relativeTo cuts the project's mount point from a path the report names.
func relativeTo(p, root string) string {
	if rest, ok := strings.CutPrefix(p, root+"/"); ok {
		return rest
	}
	return p
}

package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkTestResult holds what every reader of test output promises, whatever it read:
// no negative counts, at most maxFailedCases failed cases with their details cut, and
// paths relative to the project.
func checkTestResult(t *testing.T, res TestResult) {
	t.Helper()
	if res.Tests < 0 || res.Failures < 0 || res.Errors < 0 || res.Skipped < 0 || res.More < 0 {
		t.Fatalf("negative count: %+v", res)
	}
	if !(res.Seconds >= 0) {
		t.Fatalf("seconds = %v", res.Seconds)
	}
	// The result is stored and served as JSON, which has no NaN or infinity.
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("result does not encode: %v", err)
	}
	if len(res.Failed) > maxFailedCases {
		t.Fatalf("%d failed cases", len(res.Failed))
	}
	for _, c := range res.Failed {
		if len(c.Details) > maxDetails+len("\n…") {
			t.Fatalf("details of %d bytes", len(c.Details))
		}
		if c.Line < 0 {
			t.Fatalf("line %d", c.Line)
		}
	}
}

// textSeeds are the recorded runner outputs in testdata plus a few broken ones.
func textSeeds(f *testing.F) {
	files, _ := filepath.Glob("testdata/*/*.txt")
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(raw))
	}
	for _, s := range []string{
		"",
		"1 example, 1 failure\n\n  1) x\n",
		"Ran 99999999999999999999 tests in 1.0s\n\nFAILED (failures=-1)",
		"Tests  1 failed | -2 passed (3)\n FAIL  a.test.ts > b\n",
		"Tests:       1 failed, 1 total\n  ● a › b\n\n",
		"4 runs, 4 assertions, 1 failures, 0 errors, 0 skips\n\nFailure:\nX#test_y [test/x.rb:-5]:\n",
	} {
		f.Add(s)
	}
}

func FuzzParseTestOutput(f *testing.F) {
	textSeeds(f)
	parsers := map[string]func(string, string) (TestResult, bool){
		"rspec": parseRSpec, "django": parseDjango, "js": parseJSTests, "vitest": parseVitest, "jest": parseJest, "minitest": parseMinitest,
	}
	f.Fuzz(func(t *testing.T, text string) {
		for name, parse := range parsers {
			res, ok := parse(text, "/var/www/html")
			if !ok {
				continue
			}
			t.Run(name, func(t *testing.T) { checkTestResult(t, res) })
		}
	})
}

func FuzzParseReports(f *testing.F) {
	f.Add(phpunitReport)
	f.Add(pytestReport)
	f.Add(`<TestRun><Results><UnitTestResult testId="1" testName="A" outcome="Failed" duration="00:00:01.5"><Output><ErrorInfo><Message>m</Message><StackTrace>   at X in /src/A.cs:line 7</StackTrace></ErrorInfo></Output></UnitTestResult></Results><TestDefinitions><UnitTest id="1"><TestMethod className="N.C" name="A"/></UnitTest></TestDefinitions></TestRun>`)
	f.Add(`<testsuite><testcase name="a" time="-1" file="/var/www/html/../../etc/passwd" line="-3"><failure message="x"/></testcase></testsuite>`)
	f.Fuzz(func(t *testing.T, report string) {
		if res, err := parseJUnit(strings.NewReader(report), "/var/www/html"); err == nil {
			checkTestResult(t, res)
		}
		if res, err := parseTRX(strings.NewReader(report), "/src"); err == nil {
			checkTestResult(t, res)
		}
	})
}

package project

import (
	"os"
	"reflect"
	"testing"
)

// The samples are real runs through the Tests section (ANSI codes stripped, as
// FinishTestRun keeps them): RSpec 3 progress output, Django 5 manage.py test, Vitest 3
// and Jest 30, each with a passing, a failing, a throwing and a skipped test.
func TestParseTestOutput(t *testing.T) {
	for _, c := range []struct {
		file  string
		parse func(string, string) (TestResult, bool)
		want  TestResult
	}{
		{"rspec/progress.txt", parseRSpec, TestResult{Report: true, Tests: 4, Failures: 1, Errors: 1, Skipped: 1, Seconds: 0.01144, Failed: []TestCase{
			{Name: "Post fails on purpose", File: "spec/models/post_spec.rb", Line: 8, Kind: "failure", Message: "expected: 2"},
			{Name: "Post raises", File: "spec/models/post_spec.rb", Line: 12, Kind: "error", Message: "RuntimeError: boom"},
		}}},
		{"django/manage-py-test.txt", parseDjango, TestResult{Report: true, Tests: 4, Failures: 1, Errors: 1, Skipped: 1, Seconds: 0.002, Failed: []TestCase{
			{Name: "test_raises", Class: "blog.tests.PostTests", File: "blog/tests.py", Line: 13, Kind: "error", Message: "RuntimeError: boom"},
			{Name: "test_fails", Class: "blog.tests.PostTests", File: "blog/tests.py", Line: 10, Kind: "failure", Message: "AssertionError: 1 != 2"},
		}}},
		{"vitest/run.txt", parseJSTests, TestResult{Report: true, Tests: 4, Failures: 1, Errors: 1, Skipped: 1, Seconds: 0.171, Failed: []TestCase{
			{Name: "fails on purpose", Class: "sum", File: "src/sum.test.ts", Line: 9, Kind: "failure", Message: "AssertionError: expected 3 to be 4 // Object.is equality"},
			{Name: "throws", Class: "sum", File: "src/sum.test.ts", Line: 13, Kind: "error", Message: "Error: boom"},
		}}},
		{"jest/run.txt", parseJSTests, TestResult{Report: true, Tests: 4, Failures: 1, Errors: 1, Skipped: 1, Seconds: 0.155, Failed: []TestCase{
			{Name: "fails on purpose", Class: "sum", File: "sum.test.js", Line: 7, Kind: "failure", Message: "expect(received).toBe(expected) // Object.is equality"},
			{Name: "throws", Class: "sum", File: "sum.test.js", Line: 11, Kind: "error", Message: "boom"},
		}}},
	} {
		raw, err := os.ReadFile("testdata/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := c.parse(string(raw), appMountTarget)
		if !ok {
			t.Fatalf("%s: no result", c.file)
		}
		for i := range got.Failed {
			if got.Failed[i].Details == "" {
				t.Errorf("%s: %s has no details", c.file, got.Failed[i].Name)
			}
			got.Failed[i].Details = ""
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.file, got, c.want)
		}
	}
	if _, ok := parseJSTests("> node build.js\ndone\n", appMountTarget); ok {
		t.Error("a script without a test summary has no result")
	}
}

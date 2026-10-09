package project

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The samples in testdata/minitest are what rails test printed for a Rails 8.1
// application (minitest 6, Ruby 4.0) with one passing, two failing, one erroring and one
// skipped test: in one process, in two parallel workers and with --verbose; "pass" is a
// filtered run of the passing test.
func TestParseMinitestRails(t *testing.T) {
	failed := []TestCase{
		{Name: "test_raises", Class: "PostTest", File: "test/models/post_test.rb", Line: 12, Kind: "error", Message: "ArgumentError: boom", Details: "ArgumentError: boom\n    test/models/post_test.rb:13:in 'block in <class:PostTest>'"},
		{Name: "test_fails_on_purpose", Class: "PostTest", File: "test/models/post_test.rb", Line: 8, Kind: "failure", Message: `Expected: "a"`, Details: "Expected: \"a\"\n  Actual: \"b\""},
		{Name: "test_plain_method_failure", Class: "PostTest", File: "test/models/post_test.rb", Line: 20, Kind: "failure", Message: "plain failure"},
	}
	byName := func(cases []TestCase) map[string]TestCase {
		m := map[string]TestCase{}
		for _, c := range cases {
			m[c.Name] = c
		}
		return m
	}
	for _, sample := range []string{"fail", "parallel", "verbose"} {
		raw, err := os.ReadFile("testdata/minitest/rails-" + sample + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		res, ok := parseMinitest(string(raw), appMountTarget)
		if !ok || !res.Report || res.Tests != 5 || res.Failures != 2 || res.Errors != 1 || res.Skipped != 1 || res.Seconds <= 0 || res.More != 0 {
			t.Fatalf("%s: %+v", sample, res)
		}
		if got, want := byName(res.Failed), byName(failed); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s failed tests:\n got %+v\nwant %+v", sample, got, want)
		}
	}
	raw, _ := os.ReadFile("testdata/minitest/rails-pass.txt")
	if res, ok := parseMinitest(string(raw), appMountTarget); !ok || res.Tests != 1 || res.Failures+res.Errors != 0 || len(res.Failed) != 0 {
		t.Fatalf("pass: %+v", res)
	}
}

// Plain minitest lists the failures numbered at the end; failures that scrolled out of the
// output kept are counted as more; output without a summary (the tests did not load) is
// no result.
func TestParseMinitestEdges(t *testing.T) {
	plain := `Finished in 0.001s, 1000.0 runs/s, 1000.0 assertions/s.

  1) Failure:
CartTest#test_total [/var/www/html/test/cart_test.rb:7]:
Expected: 3
  Actual: 2

3 runs, 3 assertions, 2 failures, 0 errors, 0 skips
`
	res, ok := parseMinitest(plain, appMountTarget)
	want := TestCase{Name: "test_total", Class: "CartTest", File: "test/cart_test.rb", Line: 7, Kind: "failure", Message: "Expected: 3", Details: "Expected: 3\n  Actual: 2"}
	if !ok || len(res.Failed) != 1 || !reflect.DeepEqual(res.Failed[0], want) || res.More != 1 {
		t.Fatalf("plain minitest: %+v", res)
	}
	if _, ok := parseMinitest("test/models/post_test.rb:3: syntax error found (SyntaxError)\n", appMountTarget); ok {
		t.Fatal("no summary, no result")
	}
	long := "Error:\nPostTest#test_big:\nRuntimeError: x\n" + strings.Repeat("    frame\n", 2000) + "\n1 runs, 0 assertions, 0 failures, 1 errors, 0 skips\n"
	if res, _ := parseMinitest(long, appMountTarget); len(res.Failed) != 1 || len(res.Failed[0].Details) > maxDetails+10 {
		t.Fatalf("long backtrace: %d", len(res.Failed[0].Details))
	}
}

// Rails 8.1 prints two blank lines between a failure and the command that runs it again;
// the error, which names no location of its own, gets it from that command.
func TestParseMinitestRails81(t *testing.T) {
	raw, err := os.ReadFile("testdata/minitest/rails-8.1-blank-lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	res, ok := parseMinitest(string(raw), appMountTarget)
	if !ok || res.Tests != 3 || res.Failures != 1 || res.Errors != 1 || len(res.Failed) != 2 {
		t.Fatalf("%+v", res)
	}
	for _, c := range res.Failed {
		if c.File != "test/models/post_test.rb" || (c.Name == "test_raises" && c.Line != 12) || (c.Name == "test_fails_on_purpose" && c.Line != 8) {
			t.Errorf("location of %s: %s:%d", c.Name, c.File, c.Line)
		}
	}
}

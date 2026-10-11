package project

import (
	"strings"
	"testing"
)

const phpunitReport = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Unit" tests="3" assertions="3" errors="1" failures="1" skipped="0" time="0.02">
    <testsuite name="Tests\Unit\CartTest" file="/var/www/html/tests/Unit/CartTest.php" tests="3">
      <testcase name="test_adds_items" file="/var/www/html/tests/Unit/CartTest.php" line="12" class="Tests\Unit\CartTest" classname="Tests.Unit.CartTest" assertions="1" time="0.005"/>
      <testcase name="test_total" file="/var/www/html/tests/Unit/CartTest.php" line="20" class="Tests\Unit\CartTest" classname="Tests.Unit.CartTest" assertions="1" time="0.007">
        <failure type="PHPUnit\Framework\ExpectationFailedException">Tests\Unit\CartTest::test_total
Failed asserting that 41 matches expected 42.

/var/www/html/tests/Unit/CartTest.php:23</failure>
      </testcase>
      <testcase name="test_discount" file="/var/www/html/tests/Unit/CartTest.php" line="30" class="Tests\Unit\CartTest" assertions="1" time="0.008">
        <error type="Error">Tests\Unit\CartTest::test_discount
Error: Call to undefined method Cart::discount()</error>
      </testcase>
    </testsuite>
  </testsuite>
</testsuites>`

const pytestReport = `<?xml version="1.0" encoding="utf-8"?><testsuites><testsuite name="pytest" errors="0" failures="1" skipped="1" tests="3" time="0.1">
<testcase classname="tests.test_app" name="test_ok" time="0.01"/>
<testcase classname="tests.test_app" name="test_skip" time="0.0"><skipped type="pytest.skip" message="later">tests/test_app.py:9: later</skipped></testcase>
<testcase classname="tests.test_app" name="test_bad" time="0.02"><failure message="assert 1 == 2">#x1B[0m#x1B[94mdef#x1B[39;49;00m#x1B[90m #x1B[39;49;00m#x1B[92mtest_bad#x1B[39;49;00m():
&gt;       assert 1 == 2
E       assert 1 == 2

tests/test_app.py:12: AssertionError</failure></testcase>
</testsuite></testsuites>`

func TestParseJUnitPHPUnit(t *testing.T) {
	res, err := parseJUnit(strings.NewReader(phpunitReport), "/var/www/html")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report || res.Tests != 3 || res.Failures != 1 || res.Errors != 1 || res.Skipped != 0 || res.Seconds != 0.02 {
		t.Fatalf("totals: %+v", res)
	}
	f := res.Failed[0]
	if f.Name != "test_total" || f.Class != "Tests.Unit.CartTest" || f.File != "tests/Unit/CartTest.php" || f.Line != 20 || f.Kind != "failure" {
		t.Fatalf("failure: %+v", f)
	}
	if f.Message != "Failed asserting that 41 matches expected 42." || !strings.Contains(f.Details, "tests/Unit/CartTest.php:23") || strings.Contains(f.Details, "/var/www/html") {
		t.Fatalf("failure text: %+v", f)
	}
	if e := res.Failed[1]; e.Kind != "error" || e.Class != `Tests\Unit\CartTest` {
		t.Fatalf("error: %+v", e)
	}
}

func TestParseJUnitPytest(t *testing.T) {
	res, err := parseJUnit(strings.NewReader(pytestReport), "/var/www/html")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tests != 3 || res.Failures != 1 || res.Skipped != 1 || len(res.Failed) != 1 {
		t.Fatalf("totals: %+v", res)
	}
	// --color=yes leaves escaped colour codes in the report; the location comes from the
	// traceback's last line.
	if f := res.Failed[0]; f.Message != "assert 1 == 2" || !strings.HasPrefix(f.Details, "def test_bad():") || f.File != "tests/test_app.py" || f.Line != 12 {
		t.Fatalf("failure: %+v", f)
	}
}

// Playwright's report has no file or line attribute; the stack names the spec.
func TestParseJUnitPlaywrightLocation(t *testing.T) {
	report := `<testsuites><testsuite name="api.spec.ts" tests="1" failures="1"><testcase name="api works" classname="api.spec.ts"><failure message="api.spec.ts:8:5 api works" type="FAILURE">  Error: expect(received).toBe(expected)

      at /var/www/html/node_modules/playwright/lib/x.js:1:2
      at /var/www/html/e2e/api.spec.ts:10:53
</failure></testcase></testsuite></testsuites>`
	res, err := parseJUnit(strings.NewReader(report), "/var/www/html")
	if err != nil {
		t.Fatal(err)
	}
	if f := res.Failed[0]; f.File != "e2e/api.spec.ts" || f.Line != 10 {
		t.Fatalf("failure: %+v", f)
	}
}

func TestParseJUnitCapsFailures(t *testing.T) {
	var b strings.Builder
	b.WriteString("<testsuite>")
	for i := 0; i < maxFailedCases+5; i++ {
		b.WriteString(`<testcase name="t"><failure message="x"/></testcase>`)
	}
	b.WriteString("</testsuite>")
	res, err := parseJUnit(strings.NewReader(b.String()), "/app")
	if err != nil || len(res.Failed) != maxFailedCases || res.More != 5 || res.Failures != maxFailedCases+5 {
		t.Fatalf("%d failed, %d more, %v", len(res.Failed), res.More, err)
	}
	if _, err := parseJUnit(strings.NewReader("not xml"), "/app"); err == nil {
		t.Fatal("garbage must be an error")
	}
}

// Pest (captured from Pest 4): the name glued in front of the message, "path::name" as
// the file, the line only in the text.
func TestParseJUnitPest(t *testing.T) {
	report := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites><testsuite name="/var/www/html/phpunit.xml" tests="1" failures="1">
<testcase name="deliberately failing" file="tests/Feature/PingTest.php::deliberately failing" class="Tests\Feature\PingTest" classname="Tests.Feature.PingTest" assertions="1" time="0.06">
<failure type="PHPUnit\Framework\ExpectationFailedException">deliberately failingFailed asserting that 2 is identical to 3.
at tests/Feature/PingTest.php:14</failure></testcase></testsuite></testsuites>`
	res, err := parseJUnit(strings.NewReader(report), "/var/www/html")
	if err != nil {
		t.Fatal(err)
	}
	if f := res.Failed[0]; f.Message != "Failed asserting that 2 is identical to 3." || f.File != "tests/Feature/PingTest.php" || f.Line != 14 {
		t.Fatalf("failure: %+v", f)
	}
}

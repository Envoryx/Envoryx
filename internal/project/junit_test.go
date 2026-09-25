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
<testcase classname="tests.test_app" name="test_bad" time="0.02"><failure message="assert 1 == 2">def test_bad():
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
	if f := res.Failed[0]; f.Message != "assert 1 == 2" || !strings.Contains(f.Details, "tests/test_app.py:12: AssertionError") {
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

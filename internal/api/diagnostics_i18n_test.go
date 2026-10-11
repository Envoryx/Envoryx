package api

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/i18ntest"
)

// The diagnostics page translates a check's detail when it is one of the fixed sentences
// in diagnostics.go; details with values in them stay as the server words them.
func TestFixedDiagnosticsDetailsAreTranslated(t *testing.T) {
	src, err := os.ReadFile("diagnostics.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?:check(?:OK|Info|Warning|Error),|c\.Detail =) ("(?:[^"\\]|\\.)*")\s*$`)
	var texts []string
	for _, line := range strings.Split(string(src), "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text, err := strconv.Unquote(m[1])
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, text)
	}
	if len(texts) < 10 {
		t.Fatalf("found only %d fixed details; did diagnostics.go change shape?", len(texts))
	}
	i18ntest.RequireTranslated(t, texts...)
}

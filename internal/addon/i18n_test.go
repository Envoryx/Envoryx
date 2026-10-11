package addon

import (
	"testing"

	"github.com/envoryx/envoryx/internal/i18ntest"
)

// The web UI runs addon descriptions through t(): the shipped examples' are translated, an
// admin's own stay as written.
func TestExampleDescriptionsAreTranslated(t *testing.T) {
	var texts []string
	for _, e := range Examples() {
		texts = append(texts, e.Description)
	}
	i18ntest.RequireTranslated(t, texts...)
}

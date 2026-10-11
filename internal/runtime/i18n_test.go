package runtime

import (
	"testing"

	"github.com/envoryx/envoryx/internal/i18ntest"
)

// The web UI runs the runtime, service and PHP extension descriptions through t().
func TestDescriptionsAreTranslated(t *testing.T) {
	var texts []string
	for _, r := range Default().All() {
		texts = append(texts, r.Description)
	}
	for _, e := range PHPExtensions() {
		texts = append(texts, e.Description)
	}
	i18ntest.RequireTranslated(t, texts...)
}

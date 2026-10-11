package project

import (
	"testing"

	"github.com/envoryx/envoryx/internal/i18ntest"
)

// The web UI runs template and action descriptions through t(), so each needs a
// translation; the template notes are longer prose and stay English.
func TestTemplateAndActionDescriptionsAreTranslated(t *testing.T) {
	var texts []string
	for _, tpl := range Templates() {
		texts = append(texts, tpl.Description)
	}
	for _, a := range actionCatalog {
		texts = append(texts, a.Description)
	}
	i18ntest.RequireTranslated(t, texts...)
}

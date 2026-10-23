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

// The Workers form runs a preset's description and the label and hint of its argument
// through t(); the preset labels are mostly product names (Horizon, Sidekiq) and stay as
// they are where no translation exists.
func TestWorkerPresetTextsAreTranslated(t *testing.T) {
	var texts []string
	for _, p := range WorkerPresets() {
		texts = append(texts, p.Description, p.ArgLabel, p.ArgHint)
	}
	i18ntest.RequireTranslated(t, texts...)
}

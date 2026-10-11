// Package i18ntest lets Go tests check that fixed texts the server sends to the web UI, which
// the UI runs through t(), have a translation in every language file under web/src/i18n.
package i18ntest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Languages are the translated UI languages; English is the key itself.
var Languages = []string{"de", "es", "fr", "it", "nl", "pl", "pt", "ru", "uk"}

// RequireTranslated fails the test for every text that is missing from a language file.
func RequireTranslated(t *testing.T, texts ...string) {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "web", "src", "i18n")
	for _, lang := range Languages {
		raw, err := os.ReadFile(filepath.Join(dir, lang+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var dict map[string]string
		if err := json.Unmarshal(raw, &dict); err != nil {
			t.Fatalf("%s.json: %v", lang, err)
		}
		for _, text := range texts {
			if text == "" {
				continue
			}
			if _, ok := dict[text]; !ok {
				t.Errorf("%s.json has no translation for %q", lang, text)
			}
		}
	}
}

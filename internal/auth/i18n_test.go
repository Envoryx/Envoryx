package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/i18ntest"
	"github.com/envoryx/envoryx/internal/validate"
)

// The messages of the two-factor errors reach the UI, which translates them part by part
// ("invalid input: …").
func TestTwoFactorMessagesTranslated(t *testing.T) {
	var texts []string
	for _, err := range []error{ErrWrongPassword, ErrNoPassword, ErrFactorRequired, ErrNoFactor, ErrPasskeyContext, ErrCeremony, ErrPasskey} {
		msg := err.Error()
		if errors.Is(err, validate.ErrInvalid) {
			msg = strings.TrimPrefix(msg, validate.ErrInvalid.Error()+": ")
		}
		texts = append(texts, msg)
	}
	texts = append(texts, "the setup expired; start again", "remove your own second factors on your account page")
	i18ntest.RequireTranslated(t, texts...)
}

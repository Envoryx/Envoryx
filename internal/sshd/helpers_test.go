package sshd_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

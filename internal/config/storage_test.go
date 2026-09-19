package config

import (
	"errors"
	"testing"
)

func TestValidateStorage(t *testing.T) {
	// The temp dir is local on every CI runner we use.
	if w, err := ValidateStorage(t.TempDir(), false); err != nil || w != "" {
		t.Fatalf("local dir: %q, %v", w, err)
	}
	if c := CheckStorage("/definitely/not/here"); c.Kind != StorageUnknown {
		t.Fatalf("missing dir kind = %v", c.Kind)
	}
	var e *ErrNetworkStorage
	err := error(&ErrNetworkStorage{Dir: "/config", FS: "nfs"})
	if !errors.As(err, &e) || e.FS != "nfs" {
		t.Fatal("error type")
	}
}

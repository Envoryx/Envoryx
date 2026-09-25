package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveTreeUnlocksReadOnlyDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	locked := filepath.Join(dir, "web", "sites", "default")
	_ = os.MkdirAll(locked, 0o755)
	_ = os.WriteFile(filepath.Join(locked, "settings.php"), []byte("<?php"), 0o444)
	_ = os.Chmod(locked, 0o555)
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := removeTree(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}

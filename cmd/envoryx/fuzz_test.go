package main

import (
	"path/filepath"
	"testing"
)

// Whatever the server puts into Content-Disposition, the CLI saves to a plain file name
// in the current directory, never a path or a dot file.
func FuzzFilenameFromResponse(f *testing.F) {
	for _, s := range []string{
		`attachment; filename="shop-20261013.tar.gz"`,
		`attachment; filename="../../.bashrc"`,
		`attachment; filename*=UTF-8''..%2F..%2Fetc%2Fpasswd`,
		`attachment; filename=".ssh"`,
		`attachment; filename="a\nb"`,
		`inline`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, disposition string) {
		name := filenameFromResponse(disposition, "fallback.bin")
		if name == "fallback.bin" {
			return
		}
		if !downloadName.MatchString(name) || filepath.Base(name) != name || name[0] == '.' || name == ".." {
			t.Fatalf("%q gave the name %q", disposition, name)
		}
	})
}

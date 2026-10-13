package manifest

import (
	"bytes"
	"testing"
)

// What Parse accepts, Marshal writes back so that Parse reads the same manifest: the
// file Envoryx writes into a project must never be one it refuses on import. (Same
// means it writes out the same: an empty list and none are one thing.)
func FuzzParseMarshal(f *testing.F) {
	f.Add(full)
	f.Add("version: 1\nruntime: php\n")
	f.Add("version: 1\nruntime: node\nservices:\n  mysql: true\n")
	f.Fuzz(func(t *testing.T, data string) {
		m, err := Parse([]byte(data))
		if err != nil {
			return
		}
		out, err := Marshal(m)
		if err != nil {
			t.Fatalf("marshal what parsed: %v", err)
		}
		back, err := Parse(out)
		if err != nil {
			t.Fatalf("re-parse: %v\n%s", err, out)
		}
		again, err := Marshal(back)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, again) {
			t.Fatalf("round trip changed the manifest:\n%s\n%s", out, again)
		}
	})
}

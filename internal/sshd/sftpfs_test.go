package sshd

import (
	"os"
	"testing"
)

func TestParseStat(t *testing.T) {
	for _, tc := range []struct {
		out  string
		mode os.FileMode
		size int64
	}{
		{"81ed 21784656 1789777866\n", 0o755, 21784656},
		{"41ed 4096 1789777866", os.ModeDir | 0o755, 4096},
		{"a1ff 7 1789777866", os.ModeSymlink | 0o777, 7},
		{"21b6 0 1789777866", os.ModeIrregular | 0o666, 0}, // character device
	} {
		info, err := parseStat("x", tc.out)
		if err != nil {
			t.Fatalf("%q: %v", tc.out, err)
		}
		if info.Mode() != tc.mode || info.Size() != tc.size || info.ModTime().Unix() != 1789777866 {
			t.Errorf("%q: mode %v size %d mtime %v", tc.out, info.Mode(), info.Size(), info.ModTime())
		}
	}
	for _, bad := range []string{"", "stat: missing operand", "zz 1 2", "81ed x 2"} {
		if _, err := parseStat("x", bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
}

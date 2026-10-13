package siteimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/archivetest"
)

// FuzzExtract unpacks archive descriptions (see archivetest.Parse) as ZIP, tar and
// tar.gz, with the common root stripped as the import does, and checks that nothing
// lands outside the target and no symlink leads out of it.
func FuzzExtract(f *testing.F) {
	for _, s := range []string{
		"d|public_html/\nf|public_html/index.php|<?php\nl|public_html/link|index.php",
		"f|../escape.txt|evil",
		"f|/abs.txt|evil",
		"l|out|/tmp\nf|out/pwned.txt|x",
		"l|b|.\nl|a|b/..\nf|a/outside.txt|x",
		"f|up/through-owner-link.txt|x\nd|up/sub",
		"l|loop|loop\nf|loop/x|y",
		`f|site\..\..\win.txt|x`,
		"h|hard|/etc/passwd",
		"l|abs|/etc\nl|rel|../outside/keep.txt",
	} {
		for format := range 3 {
			f.Add(s, uint8(format))
		}
	}
	f.Fuzz(func(t *testing.T, spec string, format uint8) {
		members := archivetest.Parse(spec)
		var data []byte
		switch format % 3 {
		case 0:
			data = archivetest.Zip(members)
		case 1:
			data = archivetest.Tar(members, false)
		default:
			data = archivetest.Tar(members, true)
		}
		file := filepath.Join(t.TempDir(), "site")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sb := archivetest.NewSandbox(t)
		strip := ""
		if fmt, err := sniffFormat(file); err == nil {
			var names []string
			_ = walk(file, fmt, func(e entry) error {
				names = append(names, e.Name)
				return nil
			})
			strip = commonRoot(names)
		}
		_ = Extract(file, sb.Target, strip, os.Getuid(), os.Getgid())
		sb.Check(t)
	})
}

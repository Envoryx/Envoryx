package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/archivetest"
)

// archiveSeeds are archive descriptions (see archivetest.Parse) the fuzzers start from:
// ordinary trees and the escapes the extractors must refuse.
var archiveSeeds = []string{
	"d|app/\nf|app/index.php|<?php echo 1;\nl|app/current|index.php",
	"f|../escape.txt|evil",
	"l|out|/tmp\nf|out/pwned.txt|x",
	"f|up/through-owner-link.txt|x",
	"l|b|.\nl|a|b/..\nf|a/outside.txt|x",
	"l|loop|loop\nf|loop/x|y",
	"d|ro/\nf|ro/a|1\nf|ro/a|2",
	"h|hard|/etc/passwd",
	"l|abs|/etc\nl|rel|../outside/keep.txt",
	"f|a/b/../../../c|x",
}

func FuzzExtractArchive(f *testing.F) {
	for _, s := range archiveSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		sb := archivetest.NewSandbox(t)
		archive := filepath.Join(t.TempDir(), "a.tar.gz")
		if err := os.WriteFile(archive, archivetest.Tar(archivetest.Parse(spec), true), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = extractArchive(archive, sb.Target, os.Getuid(), os.Getgid())
		sb.Check(t)
	})
}

// FuzzUnpackBackupArchive feeds arbitrary tar streams to the reader of uploaded and
// offsite backups: it writes only flat files into its directory.
func FuzzUnpackBackupArchive(f *testing.F) {
	f.Add(string(archivetest.Tar(archivetest.Parse("f|shop-20261013-101010-abcdef12/backup.json|{}\nf|shop-20261013-101010-abcdef12/files.tar.gz|x"), false)))
	f.Add(string(archivetest.Tar(archivetest.Parse("f|x-20261013-101010-abcdef12/../backup.json|{}"), false)))
	f.Fuzz(func(t *testing.T, data string) {
		sb := archivetest.NewSandbox(t)
		_, _, _ = unpackBackupArchive(strings.NewReader(data), sb.Target)
		sb.Check(t)
	})
}

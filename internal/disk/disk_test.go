package disk

import (
	"errors"
	"testing"
)

func TestCheckAndRequire(t *testing.T) {
	dir := t.TempDir()
	us := Check(dir, dir, "/nonexistent/path")
	if len(us) != 1 || us[0].Path != dir || us[0].TotalBytes == 0 {
		t.Fatalf("usage = %+v", us)
	}
	if err := Require(dir, 0); err != nil {
		t.Fatalf("tiny requirement: %v", err)
	}
	if err := Require(dir, us[0].FreeBytes*2); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("impossible requirement: %v", err)
	}
	if err := Require("/nonexistent/path", 1<<60); err != nil {
		t.Fatalf("unknown filesystem must not block: %v", err)
	}
	if Human(1536) != "1.5 KiB" || Human(2<<30) != "2.0 GiB" || Human(7) != "7 B" {
		t.Fatalf("Human: %s %s %s", Human(1536), Human(2<<30), Human(7))
	}
	if isLow(Usage{TotalBytes: 100 << 30, FreeBytes: 10 << 30}) || !isLow(Usage{TotalBytes: 100 << 30, FreeBytes: 1 << 30}) || !isLow(Usage{TotalBytes: 1000 << 30, FreeBytes: 40 << 30}) {
		t.Fatal("threshold logic")
	}
}

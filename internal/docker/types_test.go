package docker

import "testing"

func TestOwnsAndStampInstance(t *testing.T) {
	legacy := ManagedLabels("p1", "shop", "php", "0.17.0")
	mine := StampInstance("a", legacy)
	theirs := StampInstance("b", legacy)
	if _, ok := legacy[LabelInstance]; ok {
		t.Fatal("StampInstance changed its input")
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"unlabelled (before instance labels)", legacy, true},
		{"this instance", mine, true},
		{"another instance", theirs, false},
		{"not managed", map[string]string{LabelInstance: "a"}, false},
	} {
		if got := Owns("a", tc.labels); got != tc.want {
			t.Errorf("%s: Owns = %v", tc.name, got)
		}
	}
	if got := StampInstance("", legacy); got[LabelInstance] != "" {
		t.Fatalf("no instance, no label: %v", got)
	}
	if got := StampInstance("a", nil); got[LabelInstance] != "a" {
		t.Fatalf("nil labels: %v", got)
	}
}

func TestStripANSI(t *testing.T) {
	for in, want := range map[string]string{
		"plain line": "plain line",
		"2026/10/07 \x1b[34mINFO\x1b[0m\tadmin started": "2026/10/07 INFO\tadmin started",
		"\x1b[1;31mERROR\x1b[39;49m boom":               "ERROR boom",
		"\x1b]8;;http://x\x07link\x1b]8;;\x07 done":     "link done",
		"\x1b(Bcharset \x1b[?25lcursor":                 "charset cursor",
		"\x1b[2K\x1b[1Gprogress":                        "progress",
	} {
		if got := StripANSI(in); got != want {
			t.Errorf("StripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

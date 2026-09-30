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

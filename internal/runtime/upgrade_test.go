package runtime

import "testing"

func TestUpgradesInPlace(t *testing.T) {
	c := Default()
	for _, tc := range []struct {
		key, from, to string
		ok            bool
	}{
		{"mongodb", "8", "8.2", true},
		{"mongodb", "7", "8.2", false}, // 8.2 wants FCV 8.0 data
		{"mongodb", "7", "8", false},
		{"mongodb", "8", "8", true},
		{"mariadb", "10.11", "11", true},
		{"mysql", "8.0", "8.4", true},
		{"postgresql", "16", "17", false},
		{"postgresql", "17", "17", true},
		{"mongodb", "8", "9", false},
	} {
		if got := c.UpgradesInPlace(tc.key, tc.from, tc.to); got != tc.ok {
			t.Errorf("%s %s -> %s: %v", tc.key, tc.from, tc.to, got)
		}
	}
}

func TestKernelProblemInUse(t *testing.T) {
	c := Default()
	const k = "6.19.0-31-generic"
	for version, want := range map[string]string{
		"8":   "cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); upgrade the database to MongoDB 8.2, which takes over its data",
		"7":   "cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic), and MongoDB 8.2 cannot take over its data; export it on a host with an older kernel, or remove and re-add the database (its data is lost)",
		"8.2": "",
	} {
		if got := c.KernelProblemInUse("mongodb", version, k); got != want {
			t.Errorf("mongodb %s: %q", version, got)
		}
	}
	if got := c.KernelProblemInUse("mongodb", "8", "6.8.0-1-pve"); got != "" {
		t.Errorf("older kernel: %q", got)
	}
	// A new choice still gets the plain advice.
	if got := c.KernelProblem("mongodb", "7", k); got != "cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); switch to MongoDB 8.2" {
		t.Errorf("new choice: %q", got)
	}
}

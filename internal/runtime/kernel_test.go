package runtime

import "testing"

func TestParseKernel(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"6.19.0-31-generic", 6, 19, true},
		{"6.8.0-1-pve", 6, 8, true},
		{"5.15.167.4-microsoft-standard-WSL2", 5, 15, true},
		{"6.12.24-Unraid", 6, 12, true},
		{"7.0.0-31-generic", 7, 0, true},
		{"6.19", 6, 19, true},
		{"4.19.0+", 4, 19, true},
		{"", 0, 0, false},
		{"6", 0, 0, false},
		{"unknown", 0, 0, false},
		{"v6.19.0", 0, 0, false},
	} {
		major, minor, ok := ParseKernel(tc.in)
		if major != tc.major || minor != tc.minor || ok != tc.ok {
			t.Errorf("ParseKernel(%q) = %d, %d, %v; want %d, %d, %v", tc.in, major, minor, ok, tc.major, tc.minor, tc.ok)
		}
	}
}

func TestKernelProblem(t *testing.T) {
	c := Default()
	for _, tc := range []struct {
		version, kernel string
		broken          bool
	}{
		{"8", "6.19.0-31-generic", true},
		{"7", "6.19.0-31-generic", true},
		{"8", "7.0.0-31-generic", true},
		{"8", "6.18.5-arch1-1", false},
		{"8", "6.8.0-1-pve", false},
		{"7", "5.15.167.4-microsoft-standard-WSL2", false},
		{"8", "6.12.24-Unraid", false},
		{"8", "", false},
		{"8", "garbage", false},
		{"8.2", "6.19.0-31-generic", false},
	} {
		msg := c.KernelProblem("mongodb", tc.version, tc.kernel)
		if (msg != "") != tc.broken {
			t.Errorf("mongodb %s on %q: %q", tc.version, tc.kernel, msg)
		}
	}
	want := "cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); switch to MongoDB 8.2"
	if got := c.KernelProblem("mongodb", "8", "6.19.0-31-generic"); got != want {
		t.Fatalf("message: %q", got)
	}
	if c.KernelProblem("mariadb", "11", "6.19.0-31-generic") != "" || c.KernelProblem("nope", "1", "6.19.0") != "" {
		t.Fatal("problem without a kernel limit")
	}
}

func TestForKernel(t *testing.T) {
	c := Default()
	var mongo Runtime
	for _, r := range c.ForKernel("6.19.0-31-generic") {
		if r.Key == "mongodb" {
			mongo = r
		}
	}
	for _, v := range mongo.Versions {
		broken := v.Version != "8.2"
		if v.Unavailable != broken || (v.UnavailableReason != "") != broken {
			t.Errorf("mongodb %s: unavailable %v %q", v.Version, v.Unavailable, v.UnavailableReason)
		}
	}
	// The shared catalogue stays untouched.
	r, _ := c.Get("mongodb")
	for _, v := range r.Versions {
		if v.Unavailable {
			t.Fatalf("catalogue changed: %+v", v)
		}
	}
	for _, r := range c.ForKernel("") {
		for _, v := range r.Versions {
			if v.Unavailable {
				t.Fatalf("unknown kernel marks %s %s", r.Key, v.Version)
			}
		}
	}
}

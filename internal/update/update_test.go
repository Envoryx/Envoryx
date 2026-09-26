package update

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.1", "v0.1.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "main-abc123", false},
		{"main-abc123", "v0.1.0", false},
		{"v0.2.0", "dev", false},
		{"v0.11.0", "v0.10.0-3-g73304d6", false},
		{"v0.10.0-3-g73304d6", "v0.9.0", false},
		{"0.2.0", "v0.1.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v0.2.0","html_url":"https://github.com/envoryx/envoryx/releases/tag/v0.2.0","published_at":"2026-10-01T10:00:00Z"}`)
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	c := New("v0.1.0", srv.URL, log)
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.Status()
	if !st.Enabled || !st.Release || !st.Available || st.Latest != "v0.2.0" || st.URL == "" || st.CheckedAt == nil || st.Error != "" {
		t.Fatalf("status: %+v", st)
	}
	if ua != "Envoryx/v0.1.0" {
		t.Fatalf("user agent %q", ua)
	}

	// A development build sees the latest release but never claims an update – named
	// the old way or the way git describe names it.
	for _, dev := range []string{"main-abc1234", "v0.1.0-3-g73304d6"} {
		d := New(dev, srv.URL, log)
		_ = d.Check(context.Background())
		if st := d.Status(); st.Release || st.Available || st.Latest != "v0.2.0" {
			t.Fatalf("dev status of %s: %+v", dev, st)
		}
	}

	// Failures keep the last good data and record the error.
	srv.Close()
	if err := c.Check(context.Background()); err == nil {
		t.Fatal("expected an error after the server went away")
	}
	if st := c.Status(); st.Latest != "v0.2.0" || st.Error == "" || !st.Available {
		t.Fatalf("status after failure: %+v", st)
	}

	if st := Disabled("v0.1.0").Status(); st.Enabled || st.Current != "v0.1.0" {
		t.Fatalf("disabled: %+v", st)
	}
}

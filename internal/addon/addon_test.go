package addon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

const minimal = `
name: widget
title: Widget
versions:
  - version: "1"
    image: acme/widget:1
  - version: "2"
    image: acme/widget:2
    default: true
port: 8080
webUI: true
env:
  WIDGET_PASSWORD: "{{secret.password}}"
  some.setting: "on"
secrets: [password]
volumes:
  - name: data
    path: /data
inject:
  WIDGET_URL: "http://{{host}}:{{port}}"
`

func TestParseAndRender(t *testing.T) {
	d, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Resolve(""); v.Image != "acme/widget:2" {
		t.Fatalf("default version: %+v", v)
	}
	if _, err := d.Resolve("3"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown version: %v", err)
	}
	got := RenderMap(d.Inject, map[string]string{"host": "widget", "port": "8080"})
	if strings.Join(got, ",") != "WIDGET_URL=http://widget:8080" {
		t.Fatalf("render: %v", got)
	}
	if d.HostnameOrName() != "widget" {
		t.Fatal(d.HostnameOrName())
	}
	// Every shipped example is valid (Examples panics otherwise).
	if ex := Examples(); len(ex) < 5 {
		t.Fatalf("examples: %d", len(ex))
	}
}

func TestParseRejects(t *testing.T) {
	for name, src := range map[string]string{
		"unknown field (e.g. privileged)": minimal + "privileged: true\n",
		"host mount field":                strings.Replace(minimal, "path: /data", "path: /data\n    source: /etc", 1),
		"reserved name":                   strings.Replace(minimal, "name: widget", "name: redis", 1),
		"bad name":                        strings.Replace(minimal, "name: widget", "name: Widget_1", 1),
		"undeclared secret":               strings.Replace(minimal, "{{secret.password}}", "{{secret.other}}", 1),
		"unknown variable":                strings.Replace(minimal, "{{host}}", "{{hostname}}", 1),
		"relative volume":                 strings.Replace(minimal, "path: /data", "path: data", 1),
		"no versions":                     "name: widget\nversions: []\n",
		"bad image":                       strings.Replace(minimal, "acme/widget:1", "Not An Image", 1),
		"web UI without port":             strings.Replace(minimal, "port: 8080", "port: 0", 1),
		"bad inject name":                 strings.Replace(minimal, "WIDGET_URL", "widget.url", 1),
		"two defaults":                    strings.Replace(minimal, `image: acme/widget:1`, "image: acme/widget:1\n    default: true", 1),
	} {
		if _, err := Parse([]byte(src)); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRegistry(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() (string, error) { return dir, nil })
	if l, err := r.List(); err != nil || len(l) != 0 {
		t.Fatalf("empty: %v %v", l, err)
	}
	if _, err := r.Save([]byte(minimal)); err != nil {
		t.Fatal(err)
	}
	// A broken file is listed with its error.
	if err := os.WriteFile(filepath.Join(dir, "broken.yml"), []byte("name: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, _ := r.List()
	if len(l) != 2 || l[0].Name != "broken" || l[0].Error == "" || l[1].Name != "widget" || l[1].Error != "" {
		t.Fatalf("list: %+v", l)
	}
	d, raw, err := r.Get("widget")
	if err != nil || d.Title != "Widget" || !strings.Contains(string(raw), "some.setting") {
		t.Fatalf("get: %+v %v", d, err)
	}
	if err := r.Delete("widget"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Get("widget"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if _, _, err := r.Get("../etc"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("traversal: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/widget.yml":
			_, _ = w.Write([]byte(minimal))
		case "/huge.yml":
			_, _ = w.Write(make([]byte, MaxSize+10))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	if raw, err := r.Fetch(context.Background(), srv.URL+"/widget.yml"); err != nil || string(raw) != minimal {
		t.Fatalf("fetch: %v", err)
	}
	for _, u := range []string{srv.URL + "/missing.yml", srv.URL + "/huge.yml", "file:///etc/passwd", "ftp://x/y"} {
		if _, err := r.Fetch(context.Background(), u); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

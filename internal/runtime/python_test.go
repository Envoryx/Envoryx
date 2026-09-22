package runtime

import (
	"strings"
	"testing"
)

func TestPythonPresetDefaults(t *testing.T) {
	cases := []struct {
		preset string
		port   int
		want   int
		app    string
	}{
		{"django", 0, 8000, "config.wsgi:application"},
		{"flask", 0, 5000, "app:app"},
		{"asgi", 0, 8000, "main:app"},
		{"wsgi", 0, 8000, "app:app"},
		{"module", 0, 8000, "app"},
		{"", 0, 8000, "config.wsgi:application"},
		{"flask", 8080, 8080, "app:app"}, // an explicit port wins over the preset default
	}
	for _, tc := range cases {
		c := PythonConfig{Server: true, Preset: tc.preset, Port: tc.port}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%s: %v", tc.preset, err)
		}
		if c.Port != tc.want || c.App != tc.app || c.Mode != PythonModeDev {
			t.Errorf("preset %q: got port %d app %q mode %q, want %d %q dev", tc.preset, c.Port, c.App, c.Mode, tc.want, tc.app)
		}
	}
	for _, bad := range []PythonConfig{
		{Server: true, Preset: "rails"},
		{Server: true, Mode: "staging"},
		{Server: true, App: "main:app; rm -rf /"},
		{Server: true, App: "../main:app"},
		{Server: true, App: "1abc"},
		{Server: true, Port: 80},
		{Server: true, Debug: true, DebugPort: 8000},
	} {
		if err := bad.Normalize(); err == nil {
			t.Fatalf("%+v must fail", bad)
		}
	}
	// Without the server everything about the server goes – but the debugger port stays:
	// it belongs to whatever process the developer starts, e.g. a management command in
	// the terminal, not to the container's main command.
	off := PythonConfig{Preset: "flask", Port: 9000, Debug: true}
	if err := off.Normalize(); err != nil || off != (PythonConfig{Debug: true, DebugPort: DefaultDebugpyPort}) {
		t.Fatalf("server off keeps only the debugger: %+v %v", off, err)
	}
	plain := PythonConfig{Preset: "flask", Port: 9000}
	if err := plain.Normalize(); err != nil || plain != (PythonConfig{}) {
		t.Fatalf("server off without the debugger resets everything: %+v %v", plain, err)
	}
	// The debugger port may equal a server port that is not in use.
	free := PythonConfig{Debug: true, DebugPort: 8000}
	if err := free.Normalize(); err != nil {
		t.Fatalf("debug port without a server: %v", err)
	}
	if len(PythonPresets) != 5 || PythonPresets[0].Key != "django" {
		t.Fatalf("presets: %+v", PythonPresets)
	}
}

func TestPythonCommands(t *testing.T) {
	cases := []struct {
		cfg  PythonConfig
		want string
	}{
		{PythonConfig{Server: true, Preset: "django"}, "python manage.py runserver 0.0.0.0:8000"},
		{PythonConfig{Server: true, Preset: "django", Mode: "production", App: "mysite.wsgi:application"}, "gunicorn mysite.wsgi:application --bind 0.0.0.0:8000"},
		{PythonConfig{Server: true, Preset: "flask"}, "flask --app app:app run --host 0.0.0.0 --port 5000 --debug"},
		{PythonConfig{Server: true, Preset: "flask", Mode: "production"}, "gunicorn app:app --bind 0.0.0.0:5000"},
		{PythonConfig{Server: true, Preset: "asgi", App: "api.main:app", Port: 9000}, "uvicorn api.main:app --host 0.0.0.0 --port 9000 --reload"},
		{PythonConfig{Server: true, Preset: "asgi", Mode: "production"}, "uvicorn main:app --host 0.0.0.0 --port 8000"},
		{PythonConfig{Server: true, Preset: "wsgi"}, "gunicorn app:app --bind 0.0.0.0:8000 --reload"},
		{PythonConfig{Server: true, Preset: "module", App: "server"}, "python -m server"},
	}
	for _, tc := range cases {
		c := tc.cfg
		if err := c.Normalize(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(c.Command(), " "); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.cfg, got, tc.want)
		}
	}

	// The wait guard tests for the entry file and passes the argv through "$@".
	c := PythonConfig{Server: true, Preset: "asgi", App: "api.main:app"}
	_ = c.Normalize()
	w := c.WrappedCommand()
	if w[0] != "sh" || w[1] != "-c" || !strings.Contains(w[2], "[ -e api.py ] || [ -d api ]") || !strings.HasSuffix(w[2], `exec "$@"`) || strings.Join(w[4:], " ") != strings.Join(c.Command(), " ") {
		t.Fatalf("wrapped: %q", w)
	}
	d := PythonConfig{Server: true, Preset: "django"}
	_ = d.Normalize()
	if w := d.WrappedCommand(); !strings.Contains(w[2], "[ -e manage.py ]") {
		t.Fatalf("django wait guard: %q", w)
	}
	if env := strings.Join(c.Env(), " "); !strings.Contains(env, "PORT=8000") || !strings.Contains(env, "FLASK_DEBUG=1") || !strings.Contains(env, "DJANGO_DEBUG=1") {
		t.Fatalf("env: %v", c.Env())
	}
	// "Production" has to mean the frameworks' debuggers are off, not just another server
	// binary: the Django template ties DEBUG to DJANGO_DEBUG.
	prod := PythonConfig{Server: true, Preset: "django", Mode: PythonModeProduction}
	if err := prod.Normalize(); err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(prod.Env(), " "); !strings.Contains(env, "FLASK_DEBUG=0") || !strings.Contains(env, "DJANGO_DEBUG=0") {
		t.Fatalf("production env: %v", prod.Env())
	}
}

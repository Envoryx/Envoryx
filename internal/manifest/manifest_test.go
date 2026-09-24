package manifest

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

const full = `
version: 1
name: Shop
docroot: public
web:
  server: nginx
php:
  version: "8.4"
  extensions: [intl, redis]
  memoryLimit: 512M
  displayErrors: false
  xdebug: true
  xdebugMode: trigger
database:
  type: mariadb
  version: "11.4"
redis: true
mailpit: true
opensearch:
  dashboards: true
storage:
  publicRead: false
domains: [shop.example.test]
env:
  APP_ENV: local
  APP_DEBUG: true
  CACHE_TTL: 60
secrets: [STRIPE_SECRET]
workers:
  - name: queue
    preset: laravel-queue
    arg: default
cron:
  - name: schedule
    schedule: "* * * * *"
    command: php artisan schedule:run
    timeout: 5m
    enabled: false
`

func TestParseFull(t *testing.T) {
	m, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Shop" || m.Docroot != "public" || m.Web.Server != "nginx" {
		t.Fatalf("basics: %+v", m)
	}
	if m.PHP.Version != "8.4" || m.PHP.DisplayErrors == nil || *m.PHP.DisplayErrors || !m.PHP.Xdebug {
		t.Fatalf("php: %+v", m.PHP)
	}
	if m.Redis == nil || *m.Redis != (Service{}) || m.Mailpit == nil || m.Memcached != nil {
		t.Fatalf("services: redis=%v mailpit=%v memcached=%v", m.Redis, m.Mailpit, m.Memcached)
	}
	if m.OpenSearch == nil || !m.OpenSearch.Dashboards || m.Storage.IsPublicRead() {
		t.Fatalf("opensearch/storage: %+v %+v", m.OpenSearch, m.Storage)
	}
	// Scalars that YAML would type as bool or int arrive as the text that was written.
	if m.Env["APP_DEBUG"] != "true" || m.Env["CACHE_TTL"] != "60" {
		t.Fatalf("env: %v", m.Env)
	}
	if len(m.Workers) != 1 || !m.Workers[0].IsEnabled() || m.Cron[0].IsEnabled() {
		t.Fatalf("workers/cron: %+v %+v", m.Workers, m.Cron)
	}
	if d, _ := m.Cron[0].TimeoutDuration(); d.Minutes() != 5 {
		t.Fatalf("timeout %v", d)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no version":          "name: shop\n",
		"future version":      "version: 9\n",
		"unknown key":         "version: 1\nphp:\n  verison: \"8.4\"\n",
		"unknown top key":     "version: 1\nredsi: true\n",
		"service false":       "version: 1\nredis: false\n",
		"dashboards on redis": "version: 1\nredis:\n  dashboards: true\n",
		"spa with php":        "version: 1\nphp: {}\nweb:\n  spaFallback: true\n",
		"bad env key":         "version: 1\nenv:\n  app-env: x\n",
		"env and secret":      "version: 1\nenv:\n  KEY: x\nsecrets: [KEY]\n",
		"bad domain":          "version: 1\ndomains: [\"not a host\"]\n",
		"worker twice":        "version: 1\nworkers:\n  - {name: q, preset: a}\n  - {name: q, preset: b}\n",
		"cron timeout":        "version: 1\ncron:\n  - {name: a, schedule: '* * * * *', command: x, timeout: soon}\n",
		"two documents":       "version: 1\n---\nversion: 1\n",
		"empty":               "",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(in))
			if !errors.Is(err, validate.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	m, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# Envoryx project manifest") {
		t.Fatalf("header missing:\n%s", out)
	}
	for _, want := range []string{"redis: true\n", "mailpit: true\n", "dashboards: true", "publicRead: false"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(m, back) {
		t.Fatalf("round trip changed the manifest:\n%+v\n%+v", m, back)
	}
}

func TestParseErrorsNameTheKey(t *testing.T) {
	for in, want := range map[string]string{
		"version: 1\nredsi: true\n":               "line 2: unknown key redsi",
		"version: 1\nredis:\n  verison: \"8\"\n":  "unknown key verison",
		"version: 1\nphp:\n  memorylimit: 512M\n": "line 3: unknown key memorylimit",
	} {
		_, err := Parse([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", in, want, err)
		}
	}
}

func TestLimitMemory(t *testing.T) {
	for in, want := range map[string]int{"512": 512, "512M": 512, "512MiB": 512, "2G": 2048, "1.5G": 1536, "2GB": 2048, "": 0} {
		got, err := LimitSet{Memory: in}.MemoryMB()
		if err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	if _, err := Parse([]byte("version: 1\nlimits:\n  app: {memory: lots}\n")); err == nil {
		t.Fatal("a size without a number must be refused")
	}
	if FormatMemory(2048) != "2G" || FormatMemory(1536) != "1536M" {
		t.Fatal("format")
	}
}

func TestHealthCheck(t *testing.T) {
	m, err := Parse([]byte("version: 1\nhealthcheck: /health\n"))
	if err != nil || m.HealthCheck == nil || *m.HealthCheck != (HealthCheck{Path: "/health"}) {
		t.Fatalf("short form: %+v %v", m.HealthCheck, err)
	}
	m, err = Parse([]byte("version: 1\nhealthcheck:\n  path: /up\n  status: 204\n  interval: 2m\n  timeout: 10s\n  failures: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if iv, to, err := m.HealthCheck.Seconds(); err != nil || iv != 120 || to != 10 || m.HealthCheck.Status != 204 || m.HealthCheck.Failures != 5 {
		t.Fatalf("full form: %+v %d %d %v", m.HealthCheck, iv, to, err)
	}
	for _, in := range []string{
		"healthcheck: health\n",
		"healthcheck:\n  path: /up\n  interval: 1.5s\n",
		"healthcheck:\n  path: /up\n  timeout: soon\n",
		"healthcheck:\n  path: /up\n  method: POST\n",
	} {
		if _, err := Parse([]byte("version: 1\n" + in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	if FormatSeconds(120) != "2m" || FormatSeconds(45) != "45s" || FormatSeconds(0) != "" {
		t.Fatal("format")
	}
}

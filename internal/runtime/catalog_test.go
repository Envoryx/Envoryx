package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestResolve(t *testing.T) {
	c := Default()
	v, err := c.Resolve("php", "")
	if err != nil || v.Version != "8.5" || !strings.HasPrefix(v.Image, "ghcr.io/envoryx/envoryx-php:") {
		t.Fatalf("default php: %+v %v", v, err)
	}
	if v.EOL || v.Preview {
		t.Fatal("the default version must be neither EOL nor preview")
	}
	php, _ := c.Get("php")
	defaults := 0
	for _, ver := range php.Versions {
		if ver.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("exactly one default version expected, got %d", defaults)
	}
	if _, err := c.Resolve("php", "5.6"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown version must be invalid, got %v", err)
	}
	for _, key := range []string{"mariadb", "mysql", "postgresql", "redis", "mailpit", "rabbitmq", "node", "caddy", "apache", "nginx"} {
		if _, err := c.Resolve(key, ""); err != nil {
			t.Errorf("%s must be available: %v", key, err)
		}
	}
	for _, key := range []string{"mariadb", "mysql", "postgresql"} {
		if _, ok := DialectFor(key); !ok {
			t.Errorf("dialect for %s missing", key)
		}
	}
	if _, err := c.Resolve("nope", ""); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown runtime must be invalid, got %v", err)
	}
}

func TestPHPConfigNormalizeAndINI(t *testing.T) {
	cfg := PHPConfig{DisplayErrors: true, Extensions: []string{"PDO_MYSQL", "mbstring", "gd", "gd", "opcache"}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Extensions, ",") != "gd,opcache,pdo_mysql" {
		t.Fatalf("extensions not normalised: %v", cfg.Extensions)
	}
	ini := cfg.INI("8.4")
	for _, want := range []string{"memory_limit = 256M", "extension=gd", "extension=pdo_mysql", "zend_extension=opcache", "display_errors = On"} {
		if !strings.Contains(ini, want) {
			t.Errorf("ini missing %q:\n%s", want, ini)
		}
	}
	// OPcache is compiled in from PHP 8.5 on; loading it as zend_extension fails there.
	ini85 := cfg.INI("8.5")
	if strings.Contains(ini85, "zend_extension=opcache") || !strings.Contains(ini85, "opcache.enable=1") {
		t.Errorf("8.5 ini must configure but not load opcache:\n%s", ini85)
	}
	if strings.Contains(ini, "extension=mbstring") {
		t.Error("built-in extensions must not be listed")
	}

	bad := []PHPConfig{
		{MemoryLimit: "lots"},
		{ErrorReporting: "E_ALL; system('x')"},
		{MaxExecutionTime: -1},
		{Extensions: []string{"evil"}},
	}
	for i, b := range bad {
		if err := b.Normalize(); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("case %d: expected invalid, got %v", i, err)
		}
	}
	def := DefaultPHPConfig()
	if err := def.Normalize(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	for _, e := range def.Extensions {
		found := false
		for _, known := range PHPExtensions() {
			if known.Name == e && known.Available {
				found = true
			}
		}
		if !found {
			t.Errorf("default extension %q is not available", e)
		}
	}
}

package runtime

import (
	"errors"
	"os"
	"regexp"
	"slices"
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
	for _, key := range []string{"mariadb", "mysql", "postgresql", "redis", "memcached", "mailpit", "rabbitmq", "meilisearch", "typesense", "opensearch", "opensearch-dashboards", "node", "caddy", "apache", "nginx"} {
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
	for _, want := range []string{"memory_limit = 256M", "extension=gd", "extension=pdo_mysql", "zend_extension=opcache", "display_errors = On", "max_input_vars = 5000", "opcache.max_accelerated_files=32531", "opcache.interned_strings_buffer=32"} {
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

// Every extension the UI offers to switch on has to be compiled into the PHP image, and
// the other way round: images/php/Dockerfile and PHPExtensions are kept in sync by hand.
func TestPHPExtensionsMatchTheImage(t *testing.T) {
	raw, err := os.ReadFile("../../images/php/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`ENV ENVORYX_PHP_EXTENSIONS="([^"]*)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("ENVORYX_PHP_EXTENSIONS not found in the Dockerfile")
	}
	inImage := map[string]bool{}
	for _, e := range strings.Fields(string(m[1])) {
		inImage[e] = true
	}
	delete(inImage, "xdebug") // switched on through PHPConfig.Xdebug, not the list
	for _, e := range PHPExtensions() {
		if e.BuiltIn {
			continue
		}
		if e.Available && !inImage[e.Name] {
			t.Errorf("%s is offered but not compiled into the image", e.Name)
		}
		delete(inImage, e.Name)
	}
	for e := range inImage {
		t.Errorf("%s is compiled into the image but not offered", e)
	}
}

// php_versions.json's missingExtensions may only name what the image build may skip:
// an extension from ENVORYX_PHP_EXTENSIONS (xdebug included). Built-in ones are
// guaranteed by the Dockerfile and never missing.
func TestPHPMissingExtensionsNameToggleableExtensions(t *testing.T) {
	raw, err := os.ReadFile("../../images/php/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`ENV ENVORYX_PHP_EXTENSIONS="([^"]*)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("ENVORYX_PHP_EXTENSIONS not found in the Dockerfile")
	}
	toggleable := strings.Fields(string(m[1]))
	_, versions := loadPHPVersions()
	for _, v := range versions {
		seen := map[string]bool{}
		for _, ext := range v.MissingExtensions {
			if !slices.Contains(toggleable, ext) {
				t.Errorf("PHP %s: %q in missingExtensions is not an extension of ENVORYX_PHP_EXTENSIONS", v.Version, ext)
			}
			if seen[ext] {
				t.Errorf("PHP %s: %q listed twice", v.Version, ext)
			}
			seen[ext] = true
		}
	}
}

// Every extension the catalogue calls built-in is installed by the Dockerfile when the
// upstream image lacks it (PHP 8.6 no longer compiles mbstring in).
func TestPHPBuiltInExtensionsAreGuaranteed(t *testing.T) {
	raw, err := os.ReadFile("../../images/php/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`for ext in ([a-z_ ]+); do php -m`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("the loop guaranteeing the built-in extensions is not in the Dockerfile")
	}
	guaranteed := strings.Fields(string(m[1]))
	for _, e := range PHPExtensions() {
		if e.BuiltIn && !slices.Contains(guaranteed, e.Name) {
			t.Errorf("%s is built-in in the catalogue but not guaranteed by the Dockerfile", e.Name)
		}
	}
}

func TestPHPConfigCheckVersion(t *testing.T) {
	v := Version{Version: "8.6", MissingExtensions: []string{"imagick", "redis", "xdebug"}}
	cfg := DefaultPHPConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckVersion(v); err != nil {
		t.Fatalf("the defaults must pass: %v", err)
	}
	cfg.Extensions = append(cfg.Extensions, "imagick")
	err := cfg.CheckVersion(v)
	if !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "PHP 8.6 does not ship imagick yet; switch it off") {
		t.Fatalf("one extension: %v", err)
	}
	cfg.Extensions = append(cfg.Extensions, "redis")
	cfg.Xdebug = true
	if err := cfg.CheckVersion(v); err == nil || !strings.Contains(err.Error(), "PHP 8.6 does not ship imagick, redis, Xdebug yet; switch them off") {
		t.Fatalf("several: %v", err)
	}
	if err := cfg.CheckVersion(Version{Version: "8.5"}); err != nil {
		t.Fatalf("a version with everything: %v", err)
	}
}

// A project that has an extension on from before its version lost it gets an ini
// without it rather than php-fpm warning at every start.
func TestPHPINISkipsWhatTheVersionLacks(t *testing.T) {
	missing := PHPMissingExtensions("8.6")
	if !slices.Contains(missing, "imagick") || !slices.Contains(missing, "xdebug") {
		t.Fatalf("8.6 should lack imagick and xdebug: %v", missing)
	}
	if PHPMissingExtensions("8.5") != nil {
		t.Fatal("8.5 lacks nothing")
	}
	cfg := DefaultPHPConfig()
	cfg.Extensions = append(cfg.Extensions, "imagick")
	cfg.Xdebug = true
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	ini := cfg.INI("8.6")
	if strings.Contains(ini, "imagick") || strings.Contains(ini, "xdebug") || !strings.Contains(ini, "extension=gd") {
		t.Fatalf("8.6 ini:\n%s", ini)
	}
	if ini := cfg.INI("8.5"); !strings.Contains(ini, "extension=imagick") || !strings.Contains(ini, "zend_extension=xdebug") {
		t.Fatalf("8.5 ini:\n%s", ini)
	}
}

// Dashboards always runs on OpenSearch's version, so every OpenSearch version needs one.
func TestOpenSearchDashboardsVersions(t *testing.T) {
	c := Default()
	search, _ := c.Get("opensearch")
	for _, v := range search.Versions {
		d, err := c.Resolve("opensearch-dashboards", v.Version)
		if err != nil {
			t.Fatalf("no dashboards for OpenSearch %s: %v", v.Version, err)
		}
		if !strings.HasSuffix(d.Image, strings.TrimPrefix(v.Image, "opensearchproject/opensearch")) {
			t.Errorf("dashboards %s does not match %s", d.Image, v.Image)
		}
	}
}

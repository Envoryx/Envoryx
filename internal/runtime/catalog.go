// Package runtime is the data-driven catalogue of runtimes and services Staqio can
// provision. The frontend reads it from the API; nothing here is hard-coded in the UI.
package runtime

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/seramos/staqio/internal/validate"
)

// Version describes one selectable version of a runtime or service.
type Version struct {
	Version string `json:"version"`
	Image   string `json:"image"`
	Label   string `json:"label"`
	EOL     bool   `json:"eol,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// Runtime describes a runtime family (php, node …) or a service family (caddy, mariadb …).
type Runtime struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"` // "runtime" | "webserver" | "database" | "service"
	Versions    []Version `json:"versions"`
	Available   bool      `json:"available"`   // false = planned, shown disabled in the UI
	Description string    `json:"description"` // short UI text
}

// Catalog is the full list of runtimes and services.
type Catalog struct {
	runtimes map[string]Runtime
	order    []string
}

// phpImage is the Staqio PHP runtime image (images/php/Dockerfile): the official php-fpm
// image plus all toggleable extensions compiled in but disabled by default.
const phpImage = "ghcr.io/seramos/staqio-php"

// Default returns the built-in catalogue.
func Default() *Catalog {
	c := &Catalog{runtimes: map[string]Runtime{}}
	c.add(Runtime{
		Key: "php", Name: "PHP", Kind: "runtime", Available: true,
		Description: "PHP-FPM worker with Composer, one container per project",
		Versions: []Version{
			{Version: "8.4", Image: phpImage + ":8.4", Label: "PHP 8.4", Default: true},
			{Version: "8.3", Image: phpImage + ":8.3", Label: "PHP 8.3"},
			{Version: "8.2", Image: phpImage + ":8.2", Label: "PHP 8.2"},
			{Version: "8.1", Image: phpImage + ":8.1", Label: "PHP 8.1", EOL: true},
			{Version: "8.0", Image: phpImage + ":8.0", Label: "PHP 8.0", EOL: true},
			{Version: "7.4", Image: phpImage + ":7.4", Label: "PHP 7.4", EOL: true},
		},
	})
	c.add(Runtime{
		Key: "caddy", Name: "Caddy", Kind: "webserver", Available: true,
		Description: "Project web server (static files + FastCGI to PHP)",
		Versions: []Version{
			{Version: "2", Image: "caddy:2-alpine", Label: "Caddy 2", Default: true},
		},
	})
	c.add(Runtime{
		Key: "node", Name: "Node.js", Kind: "runtime", Available: false,
		Description: "Node.js toolchain container (Phase 5)",
		Versions: []Version{
			{Version: "22", Image: "node:22-alpine", Label: "Node 22", Default: true},
			{Version: "20", Image: "node:20-alpine", Label: "Node 20"},
		},
	})
	c.add(Runtime{
		Key: "mariadb", Name: "MariaDB", Kind: "database", Available: false,
		Description: "MariaDB server with persistent volume (Phase 3)",
		Versions: []Version{
			{Version: "11", Image: "mariadb:11", Label: "MariaDB 11", Default: true},
			{Version: "10.11", Image: "mariadb:10.11", Label: "MariaDB 10.11"},
		},
	})
	c.add(Runtime{
		Key: "mysql", Name: "MySQL", Kind: "database", Available: false,
		Description: "MySQL server (Phase 6)",
		Versions: []Version{
			{Version: "8.4", Image: "mysql:8.4", Label: "MySQL 8.4", Default: true},
		},
	})
	c.add(Runtime{
		Key: "postgresql", Name: "PostgreSQL", Kind: "database", Available: false,
		Description: "PostgreSQL server (Phase 6)",
		Versions: []Version{
			{Version: "17", Image: "postgres:17-alpine", Label: "PostgreSQL 17", Default: true},
			{Version: "16", Image: "postgres:16-alpine", Label: "PostgreSQL 16"},
		},
	})
	c.add(Runtime{
		Key: "redis", Name: "Redis", Kind: "service", Available: false,
		Description: "Redis cache (Phase 6)",
		Versions: []Version{
			{Version: "8", Image: "redis:8-alpine", Label: "Redis 8", Default: true},
			{Version: "7", Image: "redis:7-alpine", Label: "Redis 7"},
		},
	})
	return c
}

func (c *Catalog) add(r Runtime) {
	c.runtimes[r.Key] = r
	c.order = append(c.order, r.Key)
}

// All returns the runtimes in display order.
func (c *Catalog) All() []Runtime {
	out := make([]Runtime, 0, len(c.order))
	for _, k := range c.order {
		out = append(out, c.runtimes[k])
	}
	return out
}

// Get returns a runtime by key.
func (c *Catalog) Get(key string) (Runtime, bool) {
	r, ok := c.runtimes[key]
	return r, ok
}

// Resolve validates that the runtime is available and the version exists, returning the
// version entry. Version "" selects the default.
func (c *Catalog) Resolve(key, version string) (Version, error) {
	r, ok := c.runtimes[key]
	if !ok {
		return Version{}, fmt.Errorf("%w: unknown runtime %q", validate.ErrInvalid, key)
	}
	if !r.Available {
		return Version{}, fmt.Errorf("%w: runtime %q is not available yet", validate.ErrInvalid, key)
	}
	if version == "" {
		for _, v := range r.Versions {
			if v.Default {
				return v, nil
			}
		}
		return r.Versions[0], nil
	}
	if err := validate.Version(version); err != nil {
		return Version{}, err
	}
	for _, v := range r.Versions {
		if v.Version == version {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %s version %q is not supported", validate.ErrInvalid, r.Name, version)
}

// PHPExtension describes an extension that can be toggled per project.
type PHPExtension struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// BuiltIn extensions are compiled into the official image and always on.
	BuiltIn bool `json:"builtIn"`
	// Available is false for extensions the Staqio PHP image does not ship (yet).
	Available bool `json:"available"`
}

// PHPExtensions lists the extensions Staqio knows about. Toggleable ones must be compiled
// into images/php/Dockerfile (STAQIO_PHP_EXTENSIONS).
func PHPExtensions() []PHPExtension {
	return []PHPExtension{
		{Name: "mbstring", Description: "Multibyte strings", BuiltIn: true, Available: true},
		{Name: "curl", Description: "cURL", BuiltIn: true, Available: true},
		{Name: "pdo_sqlite", Description: "PDO SQLite", BuiltIn: true, Available: true},
		{Name: "opcache", Description: "Opcode cache", Available: true},
		{Name: "pdo_mysql", Description: "PDO MySQL/MariaDB", Available: true},
		{Name: "mysqli", Description: "MySQL improved", Available: true},
		{Name: "pdo_pgsql", Description: "PDO PostgreSQL", Available: true},
		{Name: "gd", Description: "Image processing", Available: true},
		{Name: "intl", Description: "Internationalisation", Available: true},
		{Name: "zip", Description: "ZIP archives", Available: true},
		{Name: "bcmath", Description: "Arbitrary precision math", Available: true},
		{Name: "imagick", Description: "ImageMagick", Available: true},
	}
}

// PHPConfig is the per-project PHP configuration stored in project_services.config.
type PHPConfig struct {
	MemoryLimit       string   `json:"memoryLimit"`
	UploadMaxFilesize string   `json:"uploadMaxFilesize"`
	PostMaxSize       string   `json:"postMaxSize"`
	MaxExecutionTime  int      `json:"maxExecutionTime"`
	DisplayErrors     bool     `json:"displayErrors"`
	ErrorReporting    string   `json:"errorReporting"`
	Extensions        []string `json:"extensions"`
}

// DefaultPHPConfig returns sensible development defaults.
func DefaultPHPConfig() PHPConfig {
	return PHPConfig{
		MemoryLimit:       "256M",
		UploadMaxFilesize: "64M",
		PostMaxSize:       "64M",
		MaxExecutionTime:  120,
		DisplayErrors:     true,
		ErrorReporting:    "E_ALL",
		Extensions:        []string{"bcmath", "gd", "intl", "opcache", "pdo_mysql", "zip"},
	}
}

var (
	iniSizeRe   = regexp.MustCompile(`^[0-9]{1,6}[KMG]?$`)
	errReportRe = regexp.MustCompile(`^[A-Z_0-9 &|~^()!-]{1,128}$`)
)

// Normalize validates the configuration and fills empty fields with defaults.
func (c *PHPConfig) Normalize() error {
	def := DefaultPHPConfig()
	if c.MemoryLimit == "" {
		c.MemoryLimit = def.MemoryLimit
	}
	if c.UploadMaxFilesize == "" {
		c.UploadMaxFilesize = def.UploadMaxFilesize
	}
	if c.PostMaxSize == "" {
		c.PostMaxSize = def.PostMaxSize
	}
	if c.MaxExecutionTime == 0 {
		c.MaxExecutionTime = def.MaxExecutionTime
	}
	if c.ErrorReporting == "" {
		c.ErrorReporting = def.ErrorReporting
	}
	if c.Extensions == nil {
		c.Extensions = def.Extensions
	}
	for _, v := range []string{c.MemoryLimit, c.UploadMaxFilesize, c.PostMaxSize} {
		if v != "-1" && !iniSizeRe.MatchString(v) {
			return fmt.Errorf("%w: invalid size value %q (use e.g. 256M)", validate.ErrInvalid, v)
		}
	}
	if c.MaxExecutionTime < 0 || c.MaxExecutionTime > 86400 {
		return fmt.Errorf("%w: max_execution_time out of range", validate.ErrInvalid)
	}
	if !errReportRe.MatchString(c.ErrorReporting) {
		return fmt.Errorf("%w: invalid error_reporting expression", validate.ErrInvalid)
	}
	known := map[string]PHPExtension{}
	for _, e := range PHPExtensions() {
		known[e.Name] = e
	}
	seen := map[string]bool{}
	var exts []string
	for _, name := range c.Extensions {
		name = strings.ToLower(strings.TrimSpace(name))
		ext, ok := known[name]
		if !ok {
			return fmt.Errorf("%w: unknown PHP extension %q", validate.ErrInvalid, name)
		}
		if !ext.Available {
			return fmt.Errorf("%w: PHP extension %q is not available in this Staqio version", validate.ErrInvalid, name)
		}
		if ext.BuiltIn || seen[name] {
			continue
		}
		seen[name] = true
		exts = append(exts, name)
	}
	sort.Strings(exts)
	if exts == nil {
		exts = []string{}
	}
	c.Extensions = exts
	return nil
}

// INI renders the php.ini overrides for this configuration.
func (c PHPConfig) INI() string {
	var b strings.Builder
	b.WriteString("; Generated by Staqio - do not edit, changes are overwritten.\n")
	fmt.Fprintf(&b, "memory_limit = %s\n", c.MemoryLimit)
	fmt.Fprintf(&b, "upload_max_filesize = %s\n", c.UploadMaxFilesize)
	fmt.Fprintf(&b, "post_max_size = %s\n", c.PostMaxSize)
	fmt.Fprintf(&b, "max_execution_time = %d\n", c.MaxExecutionTime)
	if c.DisplayErrors {
		b.WriteString("display_errors = On\ndisplay_startup_errors = On\n")
	} else {
		b.WriteString("display_errors = Off\ndisplay_startup_errors = Off\n")
	}
	fmt.Fprintf(&b, "error_reporting = %s\n", c.ErrorReporting)
	b.WriteString("log_errors = On\nerror_log = /proc/self/fd/2\n")
	b.WriteString("date.timezone = UTC\n")
	for _, ext := range c.Extensions {
		if ext == "opcache" {
			b.WriteString("zend_extension=opcache\nopcache.enable=1\nopcache.enable_cli=0\nopcache.validate_timestamps=1\nopcache.revalidate_freq=0\n")
			continue
		}
		fmt.Fprintf(&b, "extension=%s\n", ext)
	}
	return b.String()
}

// FPMPool renders the php-fpm pool override that makes workers run as the given uid/gid.
func FPMPool(uid, gid int) string {
	return fmt.Sprintf(`; Generated by Staqio - do not edit, changes are overwritten.
[www]
user = %d
group = %d
listen = 9000
pm = dynamic
pm.max_children = 20
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 4
clear_env = no
catch_workers_output = yes
decorate_workers_output = no
`, uid, gid)
}

// Caddyfile renders the per-project web server configuration.
func Caddyfile(docroot string) string {
	root := "/var/www/html"
	if docroot != "" {
		root = root + "/" + docroot
	}
	return fmt.Sprintf(`# Generated by Staqio - do not edit, changes are overwritten.
{
	admin off
	auto_https off
}

:80 {
	root * %s
	encode gzip
	php_fastcgi php:9000
	file_server
	log {
		output stdout
		format console
	}
}
`, root)
}

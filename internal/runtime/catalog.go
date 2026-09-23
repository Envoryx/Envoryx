// Package runtime is the data-driven catalogue of runtimes and services Envoryx can
// provision. The frontend reads it from the API; nothing here is hard-coded in the UI.
package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// Version describes one selectable version of a runtime or service.
type Version struct {
	Version string `json:"version"`
	Image   string `json:"image"`
	Label   string `json:"label"`
	EOL     bool   `json:"eol,omitempty"`
	// Preview marks pre-release versions (RC/beta) that are not meant for production use.
	Preview bool `json:"preview,omitempty"`
	Default bool `json:"default,omitempty"`
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

//go:embed php_versions.json
var phpVersionsJSON []byte

//go:embed node_versions.json
var nodeVersionsJSON []byte

//go:embed python_versions.json
var pythonVersionsJSON []byte

// phpVersionFile is the single source of truth for supported PHP versions. The image build
// workflow (.github/workflows/php-images.yml) reads the same file for its matrix and the
// php-versions workflow updates it automatically when upstream publishes new releases.
type versionFile struct {
	Image    string `json:"image"`
	Default  string `json:"default"`
	Versions []struct {
		Version string `json:"version"`
		Base    string `json:"base"`
		Label   string `json:"label"`
		Preview bool   `json:"preview"`
		EOL     bool   `json:"eol"`
	} `json:"versions"`
}

func loadVersions(name string, raw []byte, labelPrefix string) (image string, versions []Version) {
	var f versionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		panic(fmt.Sprintf("%s is invalid: %v", name, err))
	}
	for _, v := range f.Versions {
		label := v.Label
		if label == "" {
			label = labelPrefix + " " + v.Version
		}
		versions = append(versions, Version{
			Version: v.Version,
			Image:   f.Image + ":" + v.Version,
			Label:   label,
			EOL:     v.EOL,
			Preview: v.Preview,
			Default: v.Version == f.Default,
		})
	}
	if len(versions) == 0 {
		panic(name + " defines no versions")
	}
	return f.Image, versions
}

func loadPHPVersions() (string, []Version) {
	return loadVersions("php_versions.json", phpVersionsJSON, "PHP")
}
func loadNodeVersions() (string, []Version) {
	return loadVersions("node_versions.json", nodeVersionsJSON, "Node")
}
func loadPythonVersions() (string, []Version) {
	return loadVersions("python_versions.json", pythonVersionsJSON, "Python")
}

// Default returns the built-in catalogue.
func Default() *Catalog {
	c := &Catalog{runtimes: map[string]Runtime{}}
	_, phpVersions := loadPHPVersions()
	c.add(Runtime{
		Key: "php", Name: "PHP", Kind: "runtime", Available: true,
		Description: "PHP-FPM worker with Composer, one container per project",
		Versions:    phpVersions,
	})
	c.add(Runtime{
		Key: "caddy", Name: "Caddy", Kind: "webserver", Available: true,
		Description: "Project web server: static files from the document root, FastCGI to PHP when enabled",
		Versions: []Version{
			{Version: "2", Image: "caddy:2-alpine", Label: "Caddy 2", Default: true},
		},
	})
	c.add(Runtime{
		Key: "apache", Name: "Apache", Kind: "webserver", Available: true,
		Description: "Apache httpd with mod_rewrite and .htaccess support, FastCGI to PHP when enabled",
		Versions: []Version{
			{Version: "2.4", Image: "httpd:2.4-alpine", Label: "Apache 2.4", Default: true},
		},
	})
	c.add(Runtime{
		Key: "nginx", Name: "Nginx", Kind: "webserver", Available: true,
		Description: "Nginx: static files, front-controller rewrite and FastCGI when PHP is enabled",
		Versions: []Version{
			{Version: "1", Image: "nginx:1-alpine", Label: "Nginx 1 (mainline)", Default: true},
		},
	})
	_, nodeVersions := loadNodeVersions()
	c.add(Runtime{
		Key: "node", Name: "Node.js", Kind: "runtime", Available: true,
		Description: "Node.js runtime: toolchain container (npm, pnpm, yarn via corepack) or dev server (Vite, Next.js, Nuxt …) as the project's main process",
		Versions:    nodeVersions,
	})
	_, pythonVersions := loadPythonVersions()
	c.add(Runtime{
		Key: "python", Name: "Python", Kind: "runtime", Available: true,
		Description: "Python runtime: tooling container (pip, uv, venv) or application server (Django, Flask, FastAPI/uvicorn, gunicorn) as the project's main process",
		Versions:    pythonVersions,
	})
	c.add(Runtime{
		Key: "mariadb", Name: "MariaDB", Kind: "database", Available: true,
		Description: "MariaDB server with persistent volume and generated credentials",
		Versions: []Version{
			{Version: "11", Image: "mariadb:11", Label: "MariaDB 11 (rolling)", Default: true},
			{Version: "11.4", Image: "mariadb:11.4", Label: "MariaDB 11.4 LTS"},
			{Version: "10.11", Image: "mariadb:10.11", Label: "MariaDB 10.11 LTS"},
			{Version: "10.6", Image: "mariadb:10.6", Label: "MariaDB 10.6 LTS"},
		},
	})
	c.add(Runtime{
		Key: "mysql", Name: "MySQL", Kind: "database", Available: true,
		Description: "MySQL server with persistent volume and generated credentials",
		Versions: []Version{
			{Version: "9", Image: "mysql:9", Label: "MySQL 9 (innovation)"},
			{Version: "8.4", Image: "mysql:8.4", Label: "MySQL 8.4 LTS", Default: true},
			{Version: "8.0", Image: "mysql:8.0", Label: "MySQL 8.0"},
		},
	})
	c.add(Runtime{
		Key: "postgresql", Name: "PostgreSQL", Kind: "database", Available: true,
		Description: "PostgreSQL server with persistent volume and generated credentials",
		Versions: []Version{
			{Version: "18", Image: "postgres:18-alpine", Label: "PostgreSQL 18", Default: true},
			{Version: "17", Image: "postgres:17-alpine", Label: "PostgreSQL 17"},
			{Version: "16", Image: "postgres:16-alpine", Label: "PostgreSQL 16"},
		},
	})
	c.add(Runtime{
		Key: "mongodb", Name: "MongoDB", Kind: "database", Available: true,
		Description: "MongoDB document database with persistent volume and generated credentials",
		// 8.0 and 7.0 refuse to start on Linux 6.19 and newer ("MongoDB cannot start:
		// Linux kernel versions 6.19 and newer has a known incompatibility with this
		// version", SERVER-121912), which covers current desktop and server kernels. 8.2
		// carries the fix and is therefore what a new project gets; the older series stay
		// selectable for hosts that already run them.
		Versions: []Version{
			{Version: "8.2", Image: "mongo:8.2", Label: "MongoDB 8.2", Default: true},
			{Version: "8", Image: "mongo:8.0", Label: "MongoDB 8.0"},
			{Version: "7", Image: "mongo:7.0", Label: "MongoDB 7.0"},
		},
	})
	c.add(Runtime{
		Key: "redis", Name: "Redis", Kind: "service", Available: true,
		Description: "Redis cache/queue with persistent volume (REDIS_URL injected)",
		Versions: []Version{
			{Version: "8", Image: "redis:8-alpine", Label: "Redis 8", Default: true},
			{Version: "7", Image: "redis:7-alpine", Label: "Redis 7"},
		},
	})
	c.add(Runtime{
		Key: "rabbitmq", Name: "RabbitMQ", Kind: "service", Available: true,
		Description: "RabbitMQ message broker with management UI and persistent volume (RABBITMQ_* injected)",
		Versions: []Version{
			{Version: "4.3", Image: "rabbitmq:4.3-management-alpine", Label: "RabbitMQ 4.3", Default: true},
			{Version: "4.2", Image: "rabbitmq:4.2-management-alpine", Label: "RabbitMQ 4.2"},
		},
	})
	c.add(Runtime{
		Key: "memcached", Name: "Memcached", Kind: "service", Available: true,
		Description: "Memcached in-memory cache without persistence (MEMCACHED_* injected)",
		Versions: []Version{
			{Version: "1.6", Image: "memcached:1.6-alpine", Label: "Memcached 1.6", Default: true},
		},
	})
	c.add(Runtime{
		Key: "mailpit", Name: "Mailpit", Kind: "service", Available: true,
		Description: "Catches outgoing mail (SMTP) with a web inbox (MAIL_* / MAILER_DSN injected)",
		Versions: []Version{
			{Version: "1.31", Image: "axllent/mailpit:v1.31", Label: "Mailpit 1.31", Default: true},
		},
	})
	c.add(Runtime{
		Key: "rustfs", Name: "Object storage (S3)", Kind: "storage", Available: true,
		Description: "S3-compatible object storage with a bucket per project and a web console (S3_* / AWS_* injected)",
		Versions: []Version{
			{Version: "1.0", Image: "rustfs/rustfs:1.0.0", Label: "RustFS 1.0", Default: true},
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
	// Available is false for extensions the Envoryx PHP image does not ship (yet).
	Available bool `json:"available"`
}

// PHPExtensions lists the extensions Envoryx knows about. Toggleable ones must be compiled
// into images/php/Dockerfile (ENVORYX_PHP_EXTENSIONS).
func PHPExtensions() []PHPExtension {
	return []PHPExtension{
		{Name: "mbstring", Description: "Multibyte strings", BuiltIn: true, Available: true},
		{Name: "curl", Description: "cURL", BuiltIn: true, Available: true},
		{Name: "pdo_sqlite", Description: "PDO SQLite", BuiltIn: true, Available: true},
		{Name: "opcache", Description: "Opcode cache", Available: true},
		{Name: "pdo_mysql", Description: "PDO MySQL/MariaDB", Available: true},
		{Name: "mongodb", Description: "MongoDB driver", Available: true},
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
	// Xdebug enables step debugging (zend_extension, mode debug+develop, port 9003).
	Xdebug bool `json:"xdebug"`
	// XdebugMode is "always" (every request) or "trigger" (only with the XDEBUG_TRIGGER
	// cookie/parameter set by the IDE browser extension); default always.
	XdebugMode string `json:"xdebugMode,omitempty"`
	// XdebugIDEKey is sent to the IDE (default PHPSTORM).
	XdebugIDEKey string `json:"xdebugIdeKey,omitempty"`
	// XdebugClientHost overrides the debugger host for this project; empty = discovered
	// from the request (X-Forwarded-For) with the global setting as fallback.
	XdebugClientHost string `json:"xdebugClientHost,omitempty"`
}

var ideKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

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
	c.XdebugIDEKey = strings.TrimSpace(c.XdebugIDEKey)
	if c.XdebugIDEKey == "" {
		c.XdebugIDEKey = "PHPSTORM"
	}
	c.XdebugMode = strings.ToLower(strings.TrimSpace(c.XdebugMode))
	switch c.XdebugMode {
	case "":
		c.XdebugMode = "always"
	case "always", "trigger":
	default:
		return fmt.Errorf("%w: Xdebug mode must be always or trigger", validate.ErrInvalid)
	}
	if !ideKeyRe.MatchString(c.XdebugIDEKey) {
		return fmt.Errorf("%w: invalid Xdebug IDE key", validate.ErrInvalid)
	}
	c.XdebugClientHost = strings.TrimSpace(c.XdebugClientHost)
	if c.XdebugClientHost != "" {
		if err := validate.Hostname(c.XdebugClientHost); err != nil && net.ParseIP(c.XdebugClientHost) == nil {
			return fmt.Errorf("%w: Xdebug client host must be a host name or IP", validate.ErrInvalid)
		}
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
			return fmt.Errorf("%w: PHP extension %q is not available in this Envoryx version", validate.ErrInvalid, name)
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

// opcacheBuiltIn reports whether OPcache is statically compiled into PHP (8.5+), in which
// case it must not be loaded as a zend_extension.
func opcacheBuiltIn(phpVersion string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(phpVersion, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 8 || (major == 8 && minor >= 5)
}

// INI renders the php.ini overrides for this configuration for the given PHP version.
// INIOptions are environment-dependent inputs for the generated php.ini.
type INIOptions struct {
	// XdebugClientHost is the fallback debugger host when the request does not reveal it.
	XdebugClientHost string
}

func (c PHPConfig) INI(phpVersion string) string { return c.INIWith(phpVersion, INIOptions{}) }

// INIWith renders the per-project php.ini.
func (c PHPConfig) INIWith(phpVersion string, opts INIOptions) string {
	var b strings.Builder
	b.WriteString("; Generated by Envoryx - do not edit, changes are overwritten.\n")
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
			if !opcacheBuiltIn(phpVersion) {
				b.WriteString("zend_extension=opcache\n")
			}
			b.WriteString("opcache.enable=1\nopcache.enable_cli=0\nopcache.validate_timestamps=1\nopcache.revalidate_freq=0\n")
			continue
		}
		fmt.Fprintf(&b, "extension=%s\n", ext)
	}
	if c.Xdebug {
		host := c.XdebugClientHost
		if host == "" {
			host = opts.XdebugClientHost
		}
		if host == "" {
			host = "host.docker.internal"
		}
		start := "yes"
		if c.XdebugMode == "trigger" {
			start = "trigger"
		}
		b.WriteString("zend_extension=xdebug\n")
		fmt.Fprintf(&b, "xdebug.mode=debug,develop\nxdebug.start_with_request=%s\nxdebug.client_port=9003\n", start)
		// Behind Envoryx's proxy the browser address arrives in X-Forwarded-For.
		b.WriteString("xdebug.discover_client_host=1\nxdebug.client_discovery_header=HTTP_X_FORWARDED_FOR\n")
		fmt.Fprintf(&b, "xdebug.client_host=%s\n", host)
		fmt.Fprintf(&b, "xdebug.idekey=%s\n", c.XdebugIDEKey)
		b.WriteString("xdebug.connect_timeout_ms=300\nxdebug.log_level=0\n")
	}
	return b.String()
}

// FPMPool renders the php-fpm pool override that makes workers run as the given uid/gid.
func FPMPool(uid, gid int) string {
	return fmt.Sprintf(`; Generated by Envoryx - do not edit, changes are overwritten.
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
